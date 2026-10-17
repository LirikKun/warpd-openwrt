package main

import (
	"strings"
	"testing"
)

func TestExplicitEnableDiscoversWithAutoscanDisabled(t *testing.T) {
	for _, path := range []string{"/api/proxy", "/api/config"} {
		t.Run(path, func(t *testing.T) {
			srv, _ := toggleRuntime(t)
			if err := uciSet("warpd.global.autoscan_on_start", "0"); err != nil {
				t.Fatal(err)
			}
			if err := uciSet("warpd.m2w.outer_endpoint", ""); err != nil {
				t.Fatal(err)
			}
			srv.refreshProxyPin("m2w")
			srv.cfg.mu.Lock()
			srv.cfg.AutoscanStart = false
			srv.cfg.mu.Unlock()
			stateSetEndpoints(endpoints{masque: map[string]string{}})
			body := `{"m2w_enabled":true}`
			if path == "/api/proxy" {
				body = `{"proxy":"m2w","enabled":true}`
			}
			w := toggleRequest(srv, path, body)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "pending") || !strings.Contains(w.Body.String(), `"applied":false`) {
				t.Fatalf("missing dependency must be pending, not applied: %d %s", w.Code, w.Body.String())
			}
			if srv.cfg.Snapshot().AutoscanStart {
				t.Fatal("explicit enable changed autoscan policy")
			}
			if len(srv.orch.scanCh) != 1 {
				t.Fatal("explicit enable did not queue discovery with autoscan disabled")
			}
			req := <-srv.orch.scanCh
			if len(req.targets) != 1 || req.targets[0] != "masque" {
				t.Fatalf("explicit enable queued unnecessary dependencies: %v", req.targets)
			}
		})
	}
}

func TestSupervisionApplyHonorsAutoscanStart(t *testing.T) {
	for _, autoscan := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[autoscan], func(t *testing.T) {
			srv, _ := toggleRuntime(t)
			srv.cfg.mu.Lock()
			srv.cfg.AutoscanStart = autoscan
			srv.cfg.Proxies["warp"].Enabled = true
			srv.cfg.Proxies["warp"].Endpoint = ""
			srv.cfg.mu.Unlock()
			stateSetEndpoints(endpoints{masque: map[string]string{}})
			if err := srv.orch.applyCurrent(); err == nil || !strings.Contains(err.Error(), "pending") {
				t.Fatalf("missing endpoint must remain pending: %v", err)
			} else if !autoscan && strings.Contains(err.Error(), "queued") {
				t.Fatalf("disabled discovery must not claim scans were queued: %v", err)
			}
			want := 0
			if autoscan {
				want = 1
			}
			if got := len(srv.orch.scanCh); got != want {
				t.Fatalf("AutoscanStart=%v: queued=%d want=%d", autoscan, got, want)
			}
			if !autoscan && len(srv.orch.reserved) != 0 {
				t.Fatal("disabled autoscan reserved discovery work")
			}
		})
	}
}
