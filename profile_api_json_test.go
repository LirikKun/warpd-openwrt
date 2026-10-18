package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestProfileMutationsRejectAmbiguousOrNullJSONWithoutWrites(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/profiles", `{"name":"first","name":"second","type":"warp","enabled":false,"port":4831}`},
		{"PATCH", "/api/profiles/warp", `{"enabled":null}`},
		{"PATCH", "/api/profiles/warp", `null`},
		{"PATCH", "/api/profiles/warp", `{"awg":{"jc":null}}`},
		{"PATCH", "/api/profiles/warp", `{"awg":{"jc":1,"jc":2}}`},
		{"PATCH", "/api/profiles/warp", `{"name":"ok","arbitrary_yaml":"SECRET_TEST_MARKER"}`},
		{"PATCH", "/api/profiles/warp", `{"enabled":false} {}`},
		{"PATCH", "/api/profiles/warp", `{"name":"` + strings.Repeat("x", 17000) + `"}`},
	} {
		t.Run(tc.body[:min(len(tc.body), 50)], func(t *testing.T) {
			srv := profileAPIFixture(t)
			path := srv.cfg.Snapshot().ProfilePath
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			w := profileAPIRequest(srv, tc.method, tc.path, tc.body)
			if w.Code != 400 {
				t.Fatalf("ambiguous input accepted: %d", w.Code)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected request persisted changes")
			}
			if strings.Contains(w.Body.String(), "SECRET_TEST_MARKER") {
				t.Fatal("error echoed request secret")
			}
		})
	}
}
func TestProfileReviewRejectsCaseAliasesBeforePersistence(t *testing.T) {
	awg, err := json.Marshal(DefaultAWGOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, method, path, body string }{
		{"profile_name_duplicate", "PATCH", "/api/profiles/warp", `{"name":"first","NAME":"second"}`},
		{"profile_enabled_duplicate", "PATCH", "/api/profiles/warp", `{"enabled":true,"ENABLED":false}`},
		{"profile_alias_only", "PATCH", "/api/profiles/warp", `{"NAME":"second"}`},
		{"profile_create_alias", "POST", "/api/profiles", `{"name":"first","NAME":"second","type":"warp","enabled":false,"port":4831}`},
		{"relay_duplicate", "POST", "/api/relays", `{"name":"first","NAME":"second","enabled":true,"ENABLED":false}`},
		{"relay_alias_only", "POST", "/api/relays", `{"NAME":"second","enabled":false}`},
		{"nested_awg_duplicate", "PATCH", "/api/profiles/warp", `{"awg":` + string(awg[:len(awg)-1]) + `,"JC":7}}`},
		{"nested_awg_alias_only", "PATCH", "/api/profiles/warp", `{"awg":` + strings.Replace(string(awg), `"jc":`, `"JC":`, 1) + `}`},
		{"awg_container_alias", "PATCH", "/api/profiles/warp", `{"AWG":` + string(awg) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := profileAPIFixture(t)
			before := srv.cfg.Snapshot()
			data, err := os.ReadFile(before.ProfilePath)
			if err != nil {
				t.Fatal(err)
			}
			w := profileAPIRequest(srv, tc.method, tc.path, tc.body)
			after, err := os.ReadFile(before.ProfilePath)
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || srv.cfg.Snapshot().Revision != before.Revision || !bytes.Equal(data, after) {
				t.Fatalf("case alias reached persistence: HTTP=%d revision_changed=%t disk_changed=%t", w.Code, srv.cfg.Snapshot().Revision != before.Revision, !bytes.Equal(data, after))
			}
		})
	}
}

func TestProfileAPISecurityCoversCreateEditDelete(t *testing.T) {
	for _, methodPath := range [][2]string{{"POST", "/api/profiles"}, {"PATCH", "/api/profiles/warp"}, {"DELETE", "/api/profiles/warp"}, {"POST", "/api/relays"}, {"PATCH", "/api/relays/missing"}, {"DELETE", "/api/relays/missing"}} {
		srv := profileAPIFixture(t)
		for _, tc := range []struct {
			origin, media string
			want          int
		}{{"http://evil.invalid", "application/json", 403}, {"", "text/plain", 400}} {
			req := httptest.NewRequest(methodPath[0], "http://router.local:9095"+methodPath[1], strings.NewReader(`{}`))
			req.Header.Set("Content-Type", tc.media)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("%s %s origin/media guard=%d want=%d", methodPath[0], methodPath[1], w.Code, tc.want)
			}
		}
	}
}
