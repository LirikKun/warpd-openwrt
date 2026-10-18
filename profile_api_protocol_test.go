package main

import "testing"

func TestDynamicStoreRejectsLegacyFixedSlotMutations(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/proxy", `{"proxy":"warp","enabled":true}`},
		{"/api/endpoint", `{"proxy":"warp","endpoint":"162.159.192.1:2408"}`},
		{"/api/endpoint/clear", `{"proxy":"warp"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			srv := profileAPIFixture(t)
			before := srv.cfg.Snapshot()
			w := profileAPIRequest(srv, "POST", tc.path, tc.body)
			if w.Code != 409 {
				t.Fatalf("legacy write must be rejected in dynamic mode: %d %s", w.Code, w.Body.String())
			}
			after := srv.cfg.Snapshot()
			if before.Revision != after.Revision || after.Proxies["warp"].Enabled {
				t.Fatal("legacy endpoint changed dynamic desired state")
			}
		})
	}
}
