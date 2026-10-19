package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		os.Stdout.WriteString("warpd " + version + "\n")
		return
	}

	_ = os.MkdirAll(filepath.Dir(account), 0700)

	cfg := LoadSettings()
	if err := cfg.InitProfiles(defaultProfilePath); err != nil {
		cfg.ProfileError = err.Error()
		logg.Printf("profiles: %v; proxy runtime stopped, management remains available", err)
	}
	if !cfg.Enabled {
		logg.Printf("disabled in UCI (warpd.global.enabled=0); exiting")
		return
	}

	orch := NewOrchestrator(cfg)

	// The renderer honors every manual pin. Re-render even when the scanned
	// standalone proxy is pinned: an unpinned M2W dependency may still change.
	// Ensure avoids a reload when the effective configuration is identical.
	orch.onApply = func(name string) {
		if err := orch.applyCurrent(); err != nil {
			logg.Printf("[%s] mihomo: %v", name, err)
		} else {
			logg.Printf("[%s] mihomo config updated", name)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// scan loop
	go orch.runScanLoop(ctx)

	// health checks + mihomo supervision (one process, all proxies)
	go runChecks(ctx, cfg, orch)

	// initial scan if no endpoints and nothing manual
	if cfg.AutoscanStart {
		go func() {
			time.Sleep(2 * time.Second)
			orch.queueMissing("startup")
		}()
	}

	srv := NewServer(cfg, orch)
	srv.StartRelays(ctx)
	go func() {
		<-ctx.Done()
		srv.accounts.Close()
		orch.Close()
		time.Sleep(500 * time.Millisecond)
		syscall.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		srv.accounts.Close()
		orch.Close()
		logg.Printf("web server error: %v", err)
		os.Exit(1)
	}
}

const version = "3.1.2"

// pinned reports whether the proxy has a manual UCI pin that overrides scans.
func pinned(p *ProxySettings, e endpoints, name string) bool {
	switch name {
	case "warp", "masque", "masqueh2":
		return p.Endpoint != ""
	case "m2w":
		return p.Outer != "" || p.Inner != ""
	}
	return false
}
