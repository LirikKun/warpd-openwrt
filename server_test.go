package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScanAPIHandlesAllVisibleProxyButtons(t *testing.T) {
	for _, name := range []string{"warp", "masque", "masqueh2", "m2w", "both"} {
		t.Run(name, func(t *testing.T) {
			s := statusTestSettings()
			for _, p := range s.Proxies {
				p.Enabled = true
			}
			o := NewOrchestrator(s)
			req := httptest.NewRequest(http.MethodPost, "/api/scan", bytes.NewBufferString(`{"for":"`+name+`"}`))
			w := httptest.NewRecorder()
			NewServer(s, o).handler().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("scan button %s rejected with %d: %s", name, w.Code, w.Body.String())
			}
			if len(o.scanCh) != 1 {
				t.Fatal("accepted scan was not queued")
			}
		})
	}
}
