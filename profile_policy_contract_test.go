package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestProfilePolicyStatusIncludesExactConfiguration(t *testing.T) {
	s, o := policyHarness(t, "warp")
	p := s.Proxies["warp"]
	p.PolicyMode = "strict"
	p.PolicyCountry = "DE"
	getCheck("warp").geoProbe = func(context.Context, string, int) (geoResult, error) { return geoResult{country: "DE"}, nil }
	checkProxyWith(context.Background(), s.Snapshot(), "warp", o, geoMeta)
	w := profileAPIRequest(NewServer(s, o), "GET", "/api/status", "")
	var data struct {
		Proxies map[string]map[string]any `json:"proxies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	st := data.Proxies["warp"]
	if st["policy_mode"] != "strict" || st["policy_country"] != "DE" || st["policy_country_verified"] != true {
		t.Fatalf("missing production policy contract: %+v", st)
	}
}

func TestProfilePolicyAPIRejectsInvalidWithoutWrites(t *testing.T) {
	srv := profileAPIFixture(t)
	w := profileAPIRequest(srv, "POST", "/api/profiles", `{"name":"default","type":"warp"}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	id := profileResultID(t, w)
	if srv.cfg.Proxies[id].PolicyMode != "exclusions" {
		t.Fatal("API create must default exclusions")
	}
	for _, body := range []string{
		`{"policy_mode":""}`, `{"policy_mode":"legacy"}`, `{"policy_mode":"OFF"}`, `{"policy_mode":null}`, `{"policy_mode":true}`,
		`{"policy_mode":"off","policy_mode":"strict"}`, `{"policy_mode":"off","POLICY_MODE":"strict"}`, `{"Policy_Mode":"off"}`,
		`{"policy_mode":"strict"}`, `{"policy_mode":"strict","policy_country":""}`, `{"policy_country":null}`, `{"policy_country":1}`,
		`{"policy_country":"de"}`, `{"policy_country":" DE"}`, `{"policy_country":"XX"}`, `{"policy_country":"ZZ"}`, `{"policy_country":"XK"}`, `{"policy_country":"USA"}`,
		`{"policy_country":"DE","policy_country":"NL"}`, `{"policy_country":"DE","POLICY_COUNTRY":"NL"}`, `{"Policy_Country":"DE"}`,
	} {
		t.Run(body, func(t *testing.T) {
			before := srv.cfg.Snapshot()
			raw, err := os.ReadFile(before.ProfilePath)
			if err != nil {
				t.Fatal(err)
			}
			w := profileAPIRequest(srv, "PATCH", "/api/profiles/"+id, body)
			after, err := os.ReadFile(before.ProfilePath)
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || !bytes.Equal(raw, after) || !reflect.DeepEqual(before, srv.cfg.Snapshot()) {
				t.Fatalf("invalid request wrote state: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
