package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func toggleRequest(srv *Server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	srv.handler().ServeHTTP(w, r)
	return w
}

func toggleRuntime(t *testing.T) (*Server, *MihomoMan) {
	t.Helper()
	db, _ := fakeToggleUCI(t)
	isolateStatusState(t)
	initial := map[string]string{}
	for _, key := range proxyNames {
		initial["warpd."+key+".enabled"] = "0"
	}
	initial["warpd.warp.endpoint"] = "192.0.2.1:2408"
	initial["warpd.masque.endpoint"] = "192.0.2.2:443"
	initial["warpd.masqueh2.endpoint"] = "192.0.2.3:443"
	initial["warpd.m2w.inner_endpoint"] = "192.0.2.4:2408"
	initial["warpd.m2w.outer_endpoint"] = "192.0.2.5:443"
	b, _ := json.Marshal(initial)
	if err := os.WriteFile(db, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadSettings()
	m, _ := newTestMihomo(t)
	mihomo = m
	o := NewOrchestrator(cfg)
	o.accountLoader = func() (warpAccount, error) { return warpAccount{}, nil }
	return NewServer(cfg, o), m
}

func TestToggleAPIAllCombinationsApplySynchronously(t *testing.T) {
	srv, m := toggleRuntime(t)
	for mask := 0; mask < 16; mask++ {
		patch := map[string]any{}
		for bit, key := range proxyNames {
			patch[key+"_enabled"] = mask&(1<<bit) != 0
		}
		b, _ := json.Marshal(patch)
		w := toggleRequest(srv, "/api/config", string(b))
		if w.Code != 200 {
			t.Fatalf("mask %d: %d %s", mask, w.Code, w.Body.String())
		}
		g := srv.cfg.Snapshot()
		if mask == 0 {
			if m.Running() {
				t.Fatal("all disabled kept child")
			}
			continue
		}
		if _, ok := m.AppliedSnapshot(g, stateGetEndpoints()); !ok {
			t.Fatalf("mask %d returned success before applying", mask)
		}
		m.mu.Lock()
		conf := m.lastConf
		m.mu.Unlock()
		for bit, key := range proxyNames {
			want := mask&(1<<bit) != 0
			if got := strings.Contains(conf, fmt.Sprintf("  - name: l%d\n", g.Proxies[key].Port)); got != want {
				t.Fatalf("mask %d %s listener=%v want=%v", mask, key, got, want)
			}
			st := srv.status()["proxies"].(map[string]ProxyState)[key]
			if st.Enabled != want {
				t.Fatalf("%s desired=%v want=%v", key, st.Enabled, want)
			}
		}
	}
}

func TestToggleAPIAppliesOrReportsFailure(t *testing.T) {
	for _, key := range proxyNames {
		t.Run(key, func(t *testing.T) {
			srv, m := toggleRuntime(t)
			w := toggleRequest(srv, "/api/proxy", fmt.Sprintf(`{"proxy":%q,"enabled":true}`, key))
			if w.Code != 200 {
				t.Fatalf("enable: %d %s", w.Code, w.Body.String())
			}
			if _, ok := m.AppliedSnapshot(srv.cfg.Snapshot(), stateGetEndpoints()); !ok {
				t.Fatal("successful response without verified apply")
			}
			srv.orch.accountLoader = func() (warpAccount, error) { return warpAccount{}, errors.New("account unavailable") }
			w = toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`)
			var result map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code < 400 || result["applied"] != false || result["error"] == nil || result["desired_enabled"] != true {
				t.Fatalf("false success: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestToggleAPISerializesPersistAndApply(t *testing.T) {
	srv, m := toggleRuntime(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv.orch.accountLoader = func() (warpAccount, error) { once.Do(func() { close(entered); <-release }); return warpAccount{}, nil }
	done1 := make(chan *httptest.ResponseRecorder, 1)
	go func() { done1 <- toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("enable did not reach apply")
	}
	done2 := make(chan *httptest.ResponseRecorder, 1)
	go func() { done2 <- toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":false}`) }()
	select {
	case w := <-done2:
		t.Errorf("disable escaped serialized apply: %d", w.Code)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if w := <-done1; w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := <-done2; w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if srv.cfg.Snapshot().Proxies["warp"].Enabled || m.Running() {
		t.Fatal("stale enable resurrected disabled proxy")
	}
}

func TestToggleAPIEnablingMissingEndpointReportsPendingAndOnlyQueuesMissingDependency(t *testing.T) {
	srv, _ := toggleRuntime(t)
	if err := uciSet("warpd.m2w.outer_endpoint", ""); err != nil {
		t.Fatal(err)
	}
	srv.refreshProxyPin("m2w")
	stateSetEndpoints(endpoints{warp: "192.0.2.1:2408", masque: map[string]string{}})
	w := toggleRequest(srv, "/api/proxy", `{"proxy":"m2w","enabled":true}`)
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code < 400 || result["applied"] != false || result["desired_enabled"] != true || !strings.Contains(fmt.Sprint(result["error"]), "pending") {
		t.Fatalf("missing endpoint falsely applied: %d %s", w.Code, w.Body.String())
	}
	if len(srv.orch.scanCh) != 1 {
		t.Fatal("missing dependency was not queued")
	}
	req := <-srv.orch.scanCh
	if len(req.targets) != 1 || req.targets[0] != "masque" {
		t.Fatalf("unneeded scans: %v", req.targets)
	}
}

func TestToggleAPIValidatesStrictBoundedJSON(t *testing.T) {
	_, read := fakeToggleUCI(t)
	s := statusTestSettings()
	srv := NewServer(s, NewOrchestrator(s))
	for _, body := range []string{`{}`, `null`, `{"proxy":"warp"}`, `{"proxy":"warp","enabled":null}`, `{"proxy":"warp","enabled":"false"}`, `{"proxy":"unknown","enabled":true}`, `{"proxy":"warp","enabled":false,"extra":1}`, `{"proxy":"warp","enabled":false} {}`, `{"proxy":"warp","enabled":false}` + strings.Repeat(" ", 65536)} {
		w := toggleRequest(srv, "/api/proxy", body)
		var result map[string]any
		if w.Code != 400 && w.Code != 413 {
			t.Errorf("body %.100s: code=%d %s", body, w.Code, w.Body.String())
			continue
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["error"] == nil {
			t.Errorf("missing JSON error: %s", w.Body.String())
		}
	}
	if len(read()) != 0 {
		t.Fatal("invalid request wrote UCI")
	}
}

func TestToggleAPIDisablesLastListenerWithoutAccount(t *testing.T) {
	fakeToggleUCI(t)
	isolateStatusState(t)
	s := statusTestSettings()
	srv := NewServer(s, NewOrchestrator(s))
	w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["applied"] != true {
		t.Fatalf("not truthfully applied: %s", w.Body.String())
	}
	if s.Snapshot().Proxies["warp"].Enabled || mihomo.Running() {
		t.Fatal("last proxy/process still enabled")
	}
}
