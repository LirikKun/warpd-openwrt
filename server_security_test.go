package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationRejectsCrossOriginBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site string
	}{
		{"foreign", "http://evil.example", ""},
		{"same-site foreign host", "http://other.router.local", "same-site"},
		{"wrong scheme", "https://router.local:9095", ""},
		{"wrong port", "http://router.local:9096", ""},
		{"null", "null", ""},
		{"malformed", "://broken", ""},
		{"path", "http://router.local:9095/path", ""},
		{"userinfo", "http://attacker@router.local:9095", ""},
		{"query", "http://router.local:9095?x", ""},
		{"fragment", "http://router.local:9095#x", ""},
		{"multiple origins", "http://router.local:9095 http://evil.example", ""},
		{"cross-site without origin", "", "cross-site"},
		{"cross-site despite matching origin", "http://router.local:9095", "cross-site"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := toggleRuntime(t)
			r := httptest.NewRequest(http.MethodPost, "http://router.local:9095/api/proxy", strings.NewReader(`{"proxy":"warp","enabled":true}`))
			r.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || srv.cfg.Snapshot().Proxies["warp"].Enabled || LoadSettings().Proxies["warp"].Enabled {
				t.Fatalf("cross-origin mutation accepted: status=%d persisted=%v", w.Code, LoadSettings().Proxies["warp"].Enabled)
			}
		})
	}
}

func TestMutationProtectionCoversEveryAPIMutation(t *testing.T) {
	s := statusTestSettings()
	srv := NewServer(s, NewOrchestrator(s))
	for _, path := range []string{"/api/proxy", "/api/config", "/api/scan", "/api/endpoint", "/api/endpoint/clear", "/api/restart", "/api/register"} {
		r := httptest.NewRequest(http.MethodPost, "http://router.local:9095"+path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s escaped mutation protection: %d", path, w.Code)
		}
	}
}

func TestMutationAllowsSameOriginAndNonBrowserAuth(t *testing.T) {
	srv, _ := toggleRuntime(t)
	for _, tc := range []struct {
		name, target, origin, secret, token string
		want                                int
	}{
		{"secretless curl", "http://router.local:9095", "", "", "", 200},
		{"same origin", "http://router.local:9095", "http://router.local:9095", "", "", 200},
		{"TLS actual origin", "https://router.local:9095", "https://router.local:9095", "", "", 200},
		{"IPv6 origin", "http://[::1]:9095", "http://[::1]:9095", "", "", 200},
		{"bearer curl", "http://router.local:9095", "", "test-only", "Bearer test-only", 200},
		{"bearer same origin", "http://router.local:9095", "http://router.local:9095", "test-only", "Bearer test-only", 200},
		{"missing bearer", "http://router.local:9095", "", "test-only", "", 401},
		{"wrong bearer", "http://router.local:9095", "", "test-only", "Bearer wrong", 401},
		{"bearer does not bypass origin", "http://router.local:9095", "http://evil.example", "test-only", "Bearer test-only", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.cfg.mu.Lock()
			srv.cfg.WebSecret = tc.secret
			srv.cfg.mu.Unlock()
			r := httptest.NewRequest(http.MethodPost, tc.target+"/api/proxy", strings.NewReader(`{"proxy":"warp","enabled":false}`))
			r.Header.Set("Content-Type", "application/json;charset=utf-8")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Header.Set("Authorization", tc.token)
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestMutationRejectsAmbiguousOriginAndIgnoresForwardedOrigin(t *testing.T) {
	s := statusTestSettings()
	srv := NewServer(s, NewOrchestrator(s))
	for _, origins := range [][]string{{""}, {"http://router.local:9095", "http://evil.example"}, {"https://public.example"}} {
		r := httptest.NewRequest(http.MethodPost, "http://router.local:9095/api/proxy", strings.NewReader(`{}`))
		r.Header["Origin"] = origins
		r.Header.Set("Forwarded", "host=public.example;proto=https")
		r.Header.Set("X-Forwarded-Host", "public.example")
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("ambiguous/forwarded origin accepted: %v status=%d", origins, w.Code)
		}
	}
}

func TestConfigMutationRejectsTrailingJSON(t *testing.T) {
	srv, _ := toggleRuntime(t)
	for _, body := range []string{`{"warp_enabled":true} {}`, `{"warp_enabled":true}junk`, `{"warp_enabled":true}` + strings.Repeat(" ", 16<<10)} {
		w := toggleRequest(srv, "/api/config", body)
		if w.Code < 400 || srv.cfg.Snapshot().Proxies["warp"].Enabled || LoadSettings().Proxies["warp"].Enabled {
			t.Fatalf("trailing/unbounded config accepted: %d", w.Code)
		}
	}
}

func TestConfigMutationRequiresJSONMediaType(t *testing.T) {
	srv, _ := toggleRuntime(t)
	for _, path := range []string{"/api/proxy", "/api/config"} {
		for _, mediaType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonp", "application/json; charset"} {
			t.Run(path+"/"+mediaType, func(t *testing.T) {
				body := `{"warp_enabled":true}`
				if path == "/api/proxy" {
					body = `{"proxy":"warp","enabled":true}`
				}
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				r.Header.Set("Content-Type", mediaType)
				w := httptest.NewRecorder()
				srv.handler().ServeHTTP(w, r)
				if w.Code < 400 {
					t.Fatalf("invalid media type %q accepted: %d", mediaType, w.Code)
				}
			})
		}
	}
	if srv.cfg.Snapshot().Proxies["warp"].Enabled || LoadSettings().Proxies["warp"].Enabled {
		t.Fatal("invalid media type persisted desired state")
	}
}
