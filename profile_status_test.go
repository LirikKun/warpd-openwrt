package main

import (
	"testing"
)

func TestStatusSupportsAnIntentionallyEmptyProfileCollection(t *testing.T) {
	cfg := statusTestSettings()
	cfg.Proxies = map[string]*ProxySettings{}
	srv := NewServer(cfg, NewOrchestrator(cfg))
	result := srv.status()
	proxies, ok := result["proxies"].(map[string]ProxyState)
	if !ok || len(proxies) != 0 {
		t.Fatalf("status restored fixed proxies: %#v", result["proxies"])
	}
	profiles, ok := result["profiles"]
	if !ok || profiles == nil {
		t.Fatal("status lacks empty public profile collection")
	}
}
