package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLanguageRootRendersCurrentSettings(t *testing.T) {
	fakeToggleUCI(t)
	s := LoadSettings()
	srv := NewServer(s, NewOrchestrator(s))
	for _, language := range []string{"en", "ru"} {
		if _, err := s.ApplyConfigPatch(map[string]any{"web_language": language}); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `<html lang="`+language+`">`) || w.Body.String() != renderWebUI(language) {
			t.Fatalf("root ignored live language %q", language)
		}
	}
}

func TestLanguageOnlyAPIKeepsEngineAndHealthUntouched(t *testing.T) {
	srv, m := toggleRuntime(t)
	if w := toggleRequest(srv, "/api/proxy", `{"proxy":"warp","enabled":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	beforeProcess := m.Snapshot()
	health := getCheck("m2w")
	health.set(func(st *ProxyState) { st.GeoCountry = "US"; st.GeoCheck = "sentinel"; st.OKChecks = 7 })
	beforeHealth := health.snapshot()
	calls := 0
	srv.orch.accountLoader = func() (warpAccount, error) {
		calls++
		return warpAccount{}, errors.New("must not load account for language")
	}
	w := toggleRequest(srv, "/api/config", `{"web_language":"en"}`)
	if w.Code != 200 {
		t.Fatalf("language save depends on engine: %d %s", w.Code, w.Body.String())
	}
	if calls != 0 || m.Snapshot() != beforeProcess || health.snapshot().GeoCheck != beforeHealth.GeoCheck || health.snapshot().OKChecks != 7 || len(srv.orch.scanCh) != 0 {
		t.Fatal("language save disturbed engine/health/scans")
	}
	if srv.cfg.Snapshot().Language != "en" || srv.status()["config"].(map[string]any)["web_language"] != "en" {
		t.Fatal("API did not expose updated language")
	}
}

func TestLanguagePatchPersistenceFailureKeepsLiveState(t *testing.T) {
	for _, mode := range []string{"noop", "command-failure"} {
		t.Run(mode, func(t *testing.T) {
			fakeToggleUCI(t)
			s := LoadSettings()
			if mode == "noop" {
				t.Setenv("WARPD_TEST_UCI_NOOP", "1")
			} else {
				t.Setenv("WARPD_TEST_UCI_DB", t.TempDir())
			}
			if _, err := s.ApplyConfigPatch(map[string]any{"web_language": "en"}); err == nil {
				t.Fatal("unverified persistence reported success")
			}
			if s.Snapshot().Language != "ru" {
				t.Fatal("failed persistence advanced live language")
			}
		})
	}
}

func TestLanguageStatusExposesCurrentLanguage(t *testing.T) {
	srv, _ := toggleRuntime(t)
	if got := srv.status()["config"].(map[string]any)["web_language"]; got != "ru" {
		t.Fatalf("status language = %#v, want ru", got)
	}
}

func TestLanguagePatchRejectsInvalidBeforeAnyWrites(t *testing.T) {
	_, read := fakeToggleUCI(t)
	s := LoadSettings()
	for _, value := range []any{"", "EN", "de", " en", "ru\n", nil, true, float64(1), []any{"en"}} {
		if _, err := s.ApplyConfigPatch(map[string]any{"web_language": value, "check_period": float64(90)}); err == nil {
			t.Errorf("accepted invalid language %#v", value)
		}
		if len(read()) != 0 || s.Snapshot().Language != "ru" {
			t.Fatalf("invalid language %#v partially mutated configuration", value)
		}
	}
}

func TestLanguagePatchPersistsAndReloads(t *testing.T) {
	_, read := fakeToggleUCI(t)
	s := LoadSettings()
	for _, want := range []string{"en", "ru"} {
		applied, err := s.ApplyConfigPatch(map[string]any{"web_language": want})
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) != 1 || applied[0] != "web_language" {
			t.Fatalf("applied: %v", applied)
		}
		if read()["warpd.global.web_language"] != want || s.Snapshot().Language != want || LoadSettings().Language != want {
			t.Fatal("language did not persist and advance live/reloaded state")
		}
	}
}

func TestLanguageLoadAndSnapshot(t *testing.T) {
	db, _ := fakeToggleUCI(t)
	for _, tc := range []struct{ stored, want string }{{"", "ru"}, {"invalid", "ru"}, {"EN", "ru"}, {" en", "ru"}, {"en ", "ru"}, {"en\n", "ru"}, {"en", "en"}, {"ru", "ru"}} {
		t.Run(tc.stored, func(t *testing.T) {
			b, _ := json.Marshal(map[string]string{"warpd.global.web_language": tc.stored})
			if err := os.WriteFile(db, b, 0600); err != nil {
				t.Fatal(err)
			}
			s := LoadSettings()
			g := s.Snapshot()
			if g.Language != tc.want {
				t.Fatalf("stored %q: got %q, want %q", tc.stored, g.Language, tc.want)
			}
		})
	}
}
