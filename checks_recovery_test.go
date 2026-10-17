package main

import (
	"errors"
	"testing"
	"time"
)

func TestSharedRecoveryRequiresCurrentFailureOfEveryServedProxy(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.Proxies["m2w"].Enabled = true
	names := []string{"warp", "m2w"}
	started := time.Now().Add(-time.Second)
	for _, name := range names {
		getCheck(name).set(func(st *ProxyState) { st.FailStreak = 3; st.LastCheck = time.Now().Format(time.RFC3339Nano) })
	}
	if !healthRoundRestartDue(s.Snapshot(), names, 0, started, time.Time{}, time.Now()) {
		t.Fatal("all fresh failed probes did not permit bounded recovery")
	}
	getCheck("warp").set(func(st *ProxyState) { st.FailStreak = 0 })
	if healthRoundRestartDue(s.Snapshot(), names, 0, started, time.Time{}, time.Now()) {
		t.Fatal("a healthy sibling did not prevent shared restart")
	}
	getCheck("warp").set(func(st *ProxyState) { st.FailStreak = 3; st.CheckedGeneration = 1 })
	if healthRoundRestartDue(s.Snapshot(), names, 0, started, time.Time{}, time.Now()) {
		t.Fatal("obsolete generation permitted restart")
	}
	getCheck("warp").set(func(st *ProxyState) { st.CheckedGeneration = 0 })
	if healthRoundRestartDue(s.Snapshot(), names, 0, started, time.Now(), time.Now()) {
		t.Fatal("recovery bypassed cooldown")
	}
	if healthRoundRestartDue(s.Snapshot(), nil, 0, started, time.Time{}, time.Now()) {
		t.Fatal("no served proxies permitted restart")
	}
	getCheck("warp").set(func(st *ProxyState) { st.LastCheck = started.Add(-time.Minute).Format(time.RFC3339Nano) })
	if healthRoundRestartDue(s.Snapshot(), names, 0, started, time.Time{}, time.Now()) {
		t.Fatal("stale failures permitted restart")
	}
}

func TestFailedCheckCountsOnlyAcceptedRescans(t *testing.T) {
	isolateStatusState(t)
	s := statusTestSettings()
	s.FailRescan = 2
	o := NewOrchestrator(s)
	for i := 0; i < 3; i++ {
		recordProxyResult(s.Snapshot(), "warp", o, exitParse{}, errors.New("fixture"), time.Now())
	}
	st := getCheck("warp").snapshot()
	if st.Checks != 3 || st.Rescans != 1 {
		t.Fatalf("counts=%d rescans=%d", st.Checks, st.Rescans)
	}
}
