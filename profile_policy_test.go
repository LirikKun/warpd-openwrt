package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProfilePolicyAPIPersistence(t *testing.T) {
	srv := profileAPIFixture(t)
	w := profileAPIRequest(srv, "POST", "/api/profiles", `{"name":"Strict WARP","type":"warp","policy_mode":"strict","policy_country":"DE"}`)
	if w.Code != 201 {
		t.Fatalf("strict create=%d %s", w.Code, w.Body.String())
	}
	id := profileResultID(t, w)
	loaded := statusTestSettings()
	if err := loaded.InitProfiles(srv.cfg.Snapshot().ProfilePath); err != nil {
		t.Fatal(err)
	}
	srv = NewServer(loaded, NewOrchestrator(loaded))
	w = profileAPIRequest(srv, "GET", "/api/profiles", "")
	var result struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range result.Profiles {
		if p["id"] == id {
			found = true
			if p["policy_mode"] != "strict" || p["policy_country"] != "DE" {
				t.Fatalf("lost policy: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("missing profile")
	}
	raw, err := os.ReadFile(loaded.ProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var profiles map[string]map[string]any
	if err = json.Unmarshal(doc["profiles"], &profiles); err != nil {
		t.Fatal(err)
	}
	if profiles[id]["policy_mode"] != "strict" || profiles[id]["policy_country"] != "DE" {
		t.Fatal("private schema must use exact policy JSON names")
	}
}
