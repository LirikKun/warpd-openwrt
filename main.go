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
	if !cfg.Enabled {
		logg.Printf("disabled in UCI (warpd.global.enabled=0); exiting")
		return
	}

	orch := NewOrchestrator(cfg)

	// when a scan lands new endpoints, re-render the mihomo config and
	// reload mihomo (unless the proxy is manually pinned in UCI)
	orch.onApply = func(name string) {
		g := cfg.Snapshot()
		if name != "both" {
			if p, ok := g.Proxies[name]; ok && pinned(p, stateGetEndpoints(), name) {
				logg.Printf("[%s] manual endpoint pinned, not switching", name)
				return
			}
		}
		acc, err := loadAccount(account)
		if err != nil {
			logg.Printf("[%s] account: %v", name, err)
			return
		}
		if err := mihomo.Ensure(g, stateGetEndpoints(), acc); err != nil {
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
			e := stateGetEndpoints()
			g := cfg.Snapshot()
			need := false
			if g.Proxies["warp"].Enabled && e.warp == "" && g.Proxies["warp"].Endpoint == "" {
				need = true
			}
			for _, k := range []string{"masque", "masqueh2"} {
				if g.Proxies[k].Enabled && e.masque[k] == "" && g.Proxies[k].Endpoint == "" {
					need = true
				}
			}
			if need {
				orch.ForceScan("both", "startup")
			}
		}()
	}

	srv := NewServer(cfg, orch)
	go func() {
		<-ctx.Done()
		mihomo.Stop()
		time.Sleep(500 * time.Millisecond)
		syscall.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		logg.Printf("web server error: %v", err)
		os.Exit(1)
	}
}

const version = "2.0.0-mihomo"

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
