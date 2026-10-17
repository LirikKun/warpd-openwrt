package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Settings is the UCI-backed configuration of warpd.
// UCI layout (/etc/config/warpd):
//
//	config warpd 'global'
//	    option enabled '1'
//	    option check_period '300'      # seconds between exit checks
//	    option scan_sample '2'         # warpscout -n
//	    option scan_jobs '6'           # warpscout -jt
//	    option web_listen '0.0.0.0'
//	    option web_port '9095'
//	    option web_secret ''           # optional bearer token for the API
//	    option autoscan_on_start '1'
//	    option fail_restart '3'        # failed checks before process restart
//	    option fail_rescan '5'         # failed checks before rescan
//	    option dme_rescan_gap '600'    # min seconds between DME-triggered rescans
//
//	config warp 'warp'
//	    option enabled '1'
//	    option listen '0.0.0.0'
//	    option port '4001'
//	    option endpoint ''             # manual pin, '' = auto (scan result)
//	    option protocol 'awg'
//	    option gen_i1 ''
//
//	    option enabled '1'
//	    option listen '0.0.0.0'
//	    option port '4002'
//	    option outer_endpoint ''
//	    option inner_endpoint ''
//	    option protocol 'awg'
//	    option outer_by 'ping'         # ping|speed
type Settings struct {
	mu sync.RWMutex

	Enabled        bool
	CheckPeriod    int
	ScanSample     int
	ScanJobs       int
	WebListen      string
	WebPort        int
	WebSecret      string
	AutoscanStart  bool
	FailRestart    int
	FailRescan     int
	DMERescanGap   int
	SnapMinSpacing int // minutes between ROM snapshots

	// Scan exclusion filters (UCI: warpd.scan.*).
	ExcludeNodes    string // comma-separated IATA codes, e.g. "DME,SVO"
	ExcludeCountries string // comma-separated ISO codes, e.g. "RU"

	MaxPing int // max endpoint ping (ms, 0 = unlimited) for endpoint selection

	Proxies map[string]*ProxySettings
}

type ProxySettings struct {
	Key      string
	Enabled  bool
	Listen   string
	Port     int
	Endpoint string // warp: manual pin
	Outer    string // m2w: manual pin outer (masque endpoint)
	Inner    string // m2w: manual pin inner (warp endpoint)
	Protocol string
	GenI1    string
	OuterBy  string
}

func (p *ProxySettings) defaults(key string) *ProxySettings {
	p.Key = key
	p.Enabled = false
	p.Listen = "0.0.0.0"
	p.Protocol = "awg"
	switch key {
	case "masque":
		p.Port = 4003
		p.Protocol = "masque"
	case "masqueh2":
		p.Port = 4004
		p.Protocol = "masque-h2"
	case "m2w":
		p.Port = 4005
	default:
		p.Port = 4001
	}
	p.OuterBy = "ping"
	return p
}

func (s *Settings) Snapshot() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Settings{
		Enabled:        s.Enabled,
		CheckPeriod:    s.CheckPeriod,
		ScanSample:     s.ScanSample,
		MaxPing:        s.MaxPing,
		ScanJobs:       s.ScanJobs,
		WebListen:      s.WebListen,
		WebPort:        s.WebPort,
		WebSecret:      s.WebSecret,
		AutoscanStart:  s.AutoscanStart,
		FailRestart:    s.FailRestart,
		FailRescan:     s.FailRescan,
		DMERescanGap:   s.DMERescanGap,
		SnapMinSpacing: s.SnapMinSpacing,
		ExcludeNodes:     s.ExcludeNodes,
		ExcludeCountries: s.ExcludeCountries,
		Proxies:        map[string]*ProxySettings{},
	}
	for k, v := range s.Proxies {
		cp := *v
		out.Proxies[k] = &cp
	}
	return out
}

// ---- UCI plumbing ----

func uciGet(key string) string {
	out, err := exec.Command("uci", "-q", "get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func uciSet(key, value string) error {
	v := strings.NewReplacer("'", "'\"'\"'").Replace(value)
	cmd := fmt.Sprintf("uci set %s='%s' && uci -q commit warpd", key, v)
	return exec.Command("sh", "-c", cmd).Run()
}

func uciBool(key string, def bool) bool {
	v := uciGet(key)
	switch strings.ToLower(v) {
	case "0", "off", "false", "no", "disabled":
		return false
	case "1", "on", "true", "yes", "enabled":
		return true
	}
	return def
}

func uciInt(key string, def, min, max int) int {
	v := uciGet(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func LoadSettings() *Settings {
	s := &Settings{
		Enabled:        uciBool("warpd.global.enabled", true),
		CheckPeriod:    uciInt("warpd.global.check_period", 300, 10, 86400),
		ScanSample:     uciInt("warpd.global.scan_sample", 2, 1, 50),
		MaxPing:        uciInt("warpd.global.max_ping", 0, 0, 5000),
		ScanJobs:       uciInt("warpd.global.scan_jobs", 6, 1, 32),
		WebListen:      defstr(uciGet("warpd.global.web_listen"), "0.0.0.0"),
		WebPort:        uciInt("warpd.global.web_port", 9095, 1024, 65535),
		WebSecret:      uciGet("warpd.global.web_secret"),
		AutoscanStart:  uciBool("warpd.global.autoscan_on_start", true),
		FailRestart:    uciInt("warpd.global.fail_restart", 3, 1, 100),
		FailRescan:     uciInt("warpd.global.fail_rescan", 5, 2, 100),
		DMERescanGap:   uciInt("warpd.global.dme_rescan_gap", 600, 60, 86400),
		SnapMinSpacing: uciInt("warpd.global.snap_spacing_min", 60, 5, 10080),
		ExcludeNodes:     uciGet("warpd.global.exclude_nodes"),
		ExcludeCountries: uciGet("warpd.global.exclude_countries"),
	}

	warp := &ProxySettings{}
	warp.defaults("warp")
	if uciGet("warpd.warp.port") != "" || uciGet("warpd.warp.enabled") != "" || uciGet("warpd.warp.endpoint") != "" {
		warp.Enabled = uciBool("warpd.warp.enabled", true)
		warp.Listen = defstr(uciGet("warpd.warp.listen"), warp.Listen)
		warp.Port = uciInt("warpd.warp.port", warp.Port, 1024, 65535)
		warp.Endpoint = uciGet("warpd.warp.endpoint")
		warp.Protocol = defstr(uciGet("warpd.warp.protocol"), "awg")
		warp.GenI1 = uciGet("warpd.warp.gen_i1")
	}

	mq := &ProxySettings{}
	mq.defaults("masque")
	if uciGet("warpd.masque.port") != "" || uciGet("warpd.masque.enabled") != "" {
		mq.Enabled = uciBool("warpd.masque.enabled", false)
		mq.Listen = defstr(uciGet("warpd.masque.listen"), mq.Listen)
		mq.Port = uciInt("warpd.masque.port", mq.Port, 1024, 65535)
		mq.Endpoint = uciGet("warpd.masque.endpoint")
	}
	mq.Protocol = "masque"

	mqh := &ProxySettings{}
	mqh.defaults("masqueh2")
	if uciGet("warpd.masqueh2.port") != "" || uciGet("warpd.masqueh2.enabled") != "" {
		mqh.Enabled = uciBool("warpd.masqueh2.enabled", false)
		mqh.Listen = defstr(uciGet("warpd.masqueh2.listen"), mqh.Listen)
		mqh.Port = uciInt("warpd.masqueh2.port", mqh.Port, 1024, 65535)
		mqh.Endpoint = uciGet("warpd.masqueh2.endpoint")
	}
	mqh.Protocol = "masque-h2"

	m2w := &ProxySettings{}
	m2w.defaults("m2w")
	if uciGet("warpd.m2w.port") != "" || uciGet("warpd.m2w.enabled") != "" {
		m2w.Enabled = uciBool("warpd.m2w.enabled", false)
		m2w.Listen = defstr(uciGet("warpd.m2w.listen"), m2w.Listen)
		m2w.Port = uciInt("warpd.m2w.port", m2w.Port, 1024, 65535)
		m2w.Outer = uciGet("warpd.m2w.outer_endpoint")
		m2w.Inner = uciGet("warpd.m2w.inner_endpoint")
	}

	s.Proxies = map[string]*ProxySettings{
		"warp":     warp,
		"masque":   mq,
		"masqueh2": mqh,
		"m2w":      m2w,
	}
	return s
}

func defstr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ApplyConfigPatch updates UCI and the live settings from a map of changes.
func (s *Settings) ApplyConfigPatch(patch map[string]any) (applied []string, err error) {
	type binding struct {
		uci  string
		kind string // int, bool, str
		min  int
		max  int
	}
	bindings := map[string]binding{
		"check_period":      {uci: "warpd.global.check_period", kind: "int", min: 10, max: 86400},
		"max_ping":          {uci: "warpd.global.max_ping", kind: "int", min: 0, max: 5000},
		"scan_sample":       {uci: "warpd.global.scan_sample", kind: "int", min: 1, max: 50},
		"scan_jobs":         {uci: "warpd.global.scan_jobs", kind: "int", min: 1, max: 32},
		"autoscan_start":    {uci: "warpd.global.autoscan_on_start", kind: "bool"},
		"fail_restart":      {uci: "warpd.global.fail_restart", kind: "int", min: 1, max: 100},
		"fail_rescan":       {uci: "warpd.global.fail_rescan", kind: "int", min: 2, max: 100},
		"dme_rescan_gap":    {uci: "warpd.global.dme_rescan_gap", kind: "int", min: 60, max: 86400},
		"exclude_nodes":     {uci: "warpd.global.exclude_nodes", kind: "str"},
		"exclude_countries": {uci: "warpd.global.exclude_countries", kind: "str"},
		"warp_port":         {uci: "warpd.warp.port", kind: "int", min: 1024, max: 65535},
		"warp_listen":       {uci: "warpd.warp.listen", kind: "str"},
		"masque_enabled":    {uci: "warpd.masque.enabled", kind: "bool"},
		"masque_port":       {uci: "warpd.masque.port", kind: "int", min: 1024, max: 65535},
		"masque_listen":     {uci: "warpd.masque.listen", kind: "str"},
		"masqueh2_enabled":  {uci: "warpd.masqueh2.enabled", kind: "bool"},
		"masqueh2_port":     {uci: "warpd.masqueh2.port", kind: "int", min: 1024, max: 65535},
		"masqueh2_listen":   {uci: "warpd.masqueh2.listen", kind: "str"},
		"m2w_enabled":       {uci: "warpd.m2w.enabled", kind: "bool"},
		"m2w_port":          {uci: "warpd.m2w.port", kind: "int", min: 1024, max: 65535},
		"web_secret":        {uci: "warpd.global.web_secret", kind: "str"},
	}
	for name, raw := range patch {
		b, ok := bindings[name]
		if !ok {
			return nil, fmt.Errorf("unknown setting: %s", name)
		}
		switch b.kind {
		case "int":
			n, err := toInt(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", name, err)
			}
			if n < b.min || n > b.max {
				return nil, fmt.Errorf("%s: %d out of range [%d,%d]", name, n, b.min, b.max)
			}
			if err := uciSet(b.uci, fmt.Sprintf("%d", n)); err != nil {
				return nil, err
			}
		case "bool":
			v, _ := raw.(bool)
			strV := "0"
			if v {
				strV = "1"
			}
			if err := uciSet(b.uci, strV); err != nil {
				return nil, err
			}
		default:
			str, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("%s: expected string", name)
			}
			if err := uciSet(b.uci, str); err != nil {
				return nil, err
			}
		}
		applied = append(applied, name)
	}
	// reload live values
	fresh := LoadSettings()
	s.mu.Lock()
	s.CheckPeriod = fresh.CheckPeriod
	s.MaxPing = fresh.MaxPing
	s.ScanSample = fresh.ScanSample
	s.ScanJobs = fresh.ScanJobs
	s.AutoscanStart = fresh.AutoscanStart
	s.FailRestart = fresh.FailRestart
	s.FailRescan = fresh.FailRescan
	s.DMERescanGap = fresh.DMERescanGap
	s.ExcludeNodes = fresh.ExcludeNodes
	s.ExcludeCountries = fresh.ExcludeCountries
	s.WebSecret = fresh.WebSecret
	for _, key := range []string{"warp", "masque", "masqueh2", "m2w"} {
		if p, ok := s.Proxies[key]; ok {
			f := fresh.Proxies[key]
			p.Enabled = f.Enabled
			p.Port = f.Port
			p.Listen = f.Listen
		}
	}
	s.mu.Unlock()
	return applied, nil
}

func toInt(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		return strconv.Atoi(v)
	}
	return 0, fmt.Errorf("expected number")
}
