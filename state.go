package main

import "time"

// ProxyState is the JSON shape served by the web UI.
type ProxyState struct {
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	Running           bool     `json:"running"`
	Ready             bool     `json:"ready"`
	Pid               int      `json:"pid"`
	Listen            string   `json:"listen"`
	Port              int      `json:"port"`
	Endpoint          string   `json:"endpoint"`
	Outer             string   `json:"outer"`
	ManualEP          bool     `json:"manual_endpoint"`
	ExitInfo          string   `json:"exit_info"`
	ExitNode          string   `json:"exit_node"`
	ExitCountry       string   `json:"exit_country"`
	LastCheck         string   `json:"last_check"`
	Checking          bool     `json:"checking"`
	CheckedGeneration uint64   `json:"checked_generation"`
	CheckedEndpoint   string   `json:"checked_endpoint"`
	CheckedOuter      string   `json:"checked_outer"`
	CheckedPort       int      `json:"checked_port"`
	CheckedListen     string   `json:"checked_listen"`
	Checks            int      `json:"checks"`
	OKChecks          int      `json:"ok_checks"`
	FailStreak        int      `json:"fail_streak"`
	Rescans           int      `json:"rescans"`
	Restarts          int      `json:"restarts"`
	Error             string   `json:"error"`
	Log               []string `json:"-"`
	_                 struct{} `json:"-"`
}

type exitParse struct {
	info, node, country string
}

var _ = time.Now
