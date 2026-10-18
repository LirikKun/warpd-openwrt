package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayAccountLegacyPublicFlagsAreExplicit(t *testing.T) {
	srv, id := credentialAPIFixture(t)
	w := profileAPIRequest(srv, "GET", "/api/profiles", "")
	var payload struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, p := range payload.Profiles {
		if p["id"] == id {
			mode, hasMode := p["credential_mode"]
			configured, hasConfigured := p["credential_configured"]
			if !hasMode || !hasConfigured || mode != "" || configured != false {
				t.Fatal("legacy Relay credential flags omitted or invented")
			}
			return
		}
	}
	t.Fatal("profile absent")
}
func TestRelayAccountRequestBudgetSupportsPinnedMaximumI1(t *testing.T) {
	// Parent's independently repaired validator supports 65,532 I1 text bytes.
	// JSON Unicode escaping can expand each ASCII source byte six-fold.
	raw := `{"awg":{"i1":"` + strings.Repeat(`\u0061`, 65532) + `"}}`
	r := httptest.NewRequest("PATCH", "/api/profiles/synthetic", bytes.NewBufferString(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	var p profilePatch
	if !decodeProfilePatch(w, r, &p) || p.AWG == nil || len(p.AWG.I1) != 65532 {
		t.Fatal("request body limit rejects supported maximum escaped I1")
	}
}
