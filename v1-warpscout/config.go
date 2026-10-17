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
//	config w2w 'w2w'
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

	Proxies map[string]*ProxySettings
}

type ProxySettings struct {
	Key      string // "warp" or "w2w"
	Enabled  bool
	Listen   string
	Port     int
	Endpoint string // warp: manual pin
	Outer    string // w2w: manual pin outer
	Inner    string // w2w: manual pin inner
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
	case "w2w":
		p.Port = 4002
	case "masque":
		p.Port = 4003
		p.Protocol = "masque"
	case "masqueh2":
		p.Port = 4004
		p.Protocol = "masque-h2"
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
		CheckPeriod:    uciInt("warpd.global.check_period", 300, 30, 86400),
		ScanSample:     uciInt("warpd.global.scan_sample", 2, 1, 50),
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

	w2w := &ProxySettings{}
	w2w.defaults("w2w")
	if uciGet("warpd.w2w.port") != "" || uciGet("warpd.w2w.enabled") != "" || uciGet("warpd.w2w.inner_endpoint") != "" {
		w2w.Enabled = uciBool("warpd.w2w.enabled", true)
		w2w.Listen = defstr(uciGet("warpd.w2w.listen"), w2w.Listen)
		w2w.Port = uciInt("warpd.w2w.port", w2w.Port, 1024, 65535)
		w2w.Outer = uciGet("warpd.w2w.outer_endpoint")
		w2w.Inner = uciGet("warpd.w2w.inner_endpoint")
		w2w.Protocol = defstr(uciGet("warpd.w2w.protocol"), "awg")
		w2w.OuterBy = defstr(uciGet("warpd.w2w.outer_by"), "ping")
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

	s.Proxies = map[string]*ProxySettings{
		"warp": warp, "w2w": w2w,
		"masque": mq, "masqueh2": mqh,
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
		"check_period":    {uci: "warpd.global.check_period", kind: "int", min: 30, max: 86400},
		"scan_sample":     {uci: "warpd.global.scan_sample", kind: "int", min: 1, max: 50},
		"scan_jobs":       {uci: "warpd.global.scan_jobs", kind: "int", min: 1, max: 32},
		"autoscan_start":  {uci: "warpd.global.autoscan_on_start", kind: "bool"},
		"fail_restart":    {uci: "warpd.global.fail_restart", kind: "int", min: 1, max: 100},
		"fail_rescan":     {uci: "warpd.global.fail_rescan", kind: "int", min: 2, max: 100},
		"dme_rescan_gap":  {uci: "warpd.global.dme_rescan_gap", kind: "int", min: 60, max: 86400},
		"exclude_nodes":     {uci: "warpd.global.exclude_nodes", kind: "str"},
		"exclude_countries": {uci: "warpd.global.exclude_countries", kind: "str"},
		"warp_port":       {uci: "warpd.warp.port", kind: "int", min: 1024, max: 65535},
		"warp_listen":     {uci: "warpd.warp.listen", kind: "str"},
		"w2w_port":        {uci: "warpd.w2w.port", kind: "int", min: 1024, max: 65535},
		"w2w_listen":      {uci: "warpd.w2w.listen", kind: "str"},
		"w2w_outer_by":    {uci: "warpd.w2w.outer_by", kind: "str"},
		"masque_enabled":  {uci: "warpd.masque.enabled", kind: "bool"},
		"masque_port":     {uci: "warpd.masque.port", kind: "int", min: 1024, max: 65535},
		"masque_listen":   {uci: "warpd.masque.listen", kind: "str"},
		"masqueh2_enabled":  {uci: "warpd.masqueh2.enabled", kind: "bool"},
		"masqueh2_port":     {uci: "warpd.masqueh2.port", kind: "int", min: 1024, max: 65535},
		"masqueh2_listen":   {uci: "warpd.masqueh2.listen", kind: "str"},
		"web_secret":      {uci: "warpd.global.web_secret", kind: "str"},
	}
	for name, raw := range patch {
		b, ok := bindings[name]
		if !ok {
			continue
		}
		switch b.kind {
		case "int":
			n, e := toInt(raw)
			if e != nil || n < b.min || n > b.max {
				return nil, fmt.Errorf("%s: expected int %d..%d", name, b.min, b.max)
			}
			if err := uciSet(b.uci, strconv.Itoa(n)); err != nil {
				return nil, err
			}
		case "bool":
			vb, e := toBool(raw)
			if e != nil {
				return nil, fmt.Errorf("%s: expected bool", name)
			}
			if err := uciSet(b.uci, boolStr(vb)); err != nil {
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
	s.ScanSample = fresh.ScanSample
	s.ScanJobs = fresh.ScanJobs
	s.AutoscanStart = fresh.AutoscanStart
	s.FailRestart = fresh.FailRestart
	s.FailRescan = fresh.FailRescan
	s.DMERescanGap = fresh.DMERescanGap
	s.ExcludeNodes = fresh.ExcludeNodes
	s.ExcludeCountries = fresh.ExcludeCountries
	s.WebSecret = fresh.WebSecret
	for _, key := range []string{"warp", "w2w", "masque", "masqueh2"} {
		if p, ok := s.Proxies[key]; ok {
			f := fresh.Proxies[key]
			p.Enabled = f.Enabled
			p.Port = f.Port
			p.Listen = f.Listen
			p.OuterBy = f.OuterBy
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
	return 0, fmt.Errorf("not a number")
}

func toBool(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(v) {
		case "1", "true", "on", "yes":
			return true, nil
		case "0", "false", "off", "no":
			return false, nil
		}
	}
	return false, fmt.Errorf("not a bool")
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
