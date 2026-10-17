package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("warpd " + version)
		return
	}

	_ = os.MkdirAll(stateDir, 0755)
	_ = os.MkdirAll(workDir, 0755)
	_ = os.MkdirAll(filepath.Dir(account), 0700)

	cfg := LoadSettings()
	if !cfg.Enabled {
		logg.Printf("disabled in UCI (warpd.global.enabled=0); exiting")
		return
	}

	orch := NewOrchestrator(cfg)

	sups := map[string]*Supervisor{
		"warp":     newSupervisor("warp", cfg),
		"w2w":      newSupervisor("w2w", cfg),
		"masque":   newSupervisor("masque", cfg),
		"masqueh2": newSupervisor("masqueh2", cfg),
	}

	// hot-switch: after a scan lands new endpoints, restart the affected
	// running proxy onto them (skipped when the proxy is manually pinned).
	orch.onApply = func(name string) {
		sup, ok := sups[name]
		if !ok {
			return
		}
		if sup.isManual() {
			logg.Printf("[%s] manual endpoint pinned, not switching", name)
			return
		}
		p := sup.cfg()
		ep, outer, manual := sup.pickEndpoints(p)
		if ep == "" {
			return
		}
		if ep == sup.currentEndpoint() && (name == "warp" || outer == sup.currentOuter()) {
			return
		}
		sup.switchEndpoint(ep, outer, manual)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// scan loop
	go orch.runScanLoop(ctx)

	// supervisors
	for _, sup := range sups {
		go sup.run(orch)
	}

	// initial scan if no endpoints and nothing manual
	if cfg.AutoscanStart {
		go func() {
			time.Sleep(2 * time.Second)
			e := stateGetEndpoints()
			g := cfg.Snapshot()
			needWarp := e.warp == "" && g.Proxies["warp"].Endpoint == "" && g.Proxies["warp"].Enabled
			needW2W := (e.w2wOuter == "" || e.w2wInner == "") &&
				g.Proxies["w2w"].Outer == "" && g.Proxies["w2w"].Inner == "" && g.Proxies["w2w"].Enabled
			if needWarp || needW2W {
				orch.ForceScan("both", "startup")
			}
		}()
	}

	srv := NewServer(cfg, orch, sups)
	go func() {
		<-ctx.Done()
		for _, sup := range sups {
			sup.stop()
		}
		time.Sleep(500 * time.Millisecond)
		syscall.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		logg.Printf("web server error: %v", err)
		os.Exit(1)
	}
}

const version = "1.0.0"

func newSupervisor(name string, cfg *Settings) *Supervisor {
	return &Supervisor{
		name:   name,
		cfg:    func() *ProxySettings { p := cfg.Snapshot().Proxies[name]; return p },
		glob:   cfg.Snapshot,
		stopCh: make(chan struct{}),
	}
}
