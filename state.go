package main

import "time"

// ProxyState is the JSON shape served by the web UI.
type ProxyState struct {
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Running           bool   `json:"running"`
	Ready             bool   `json:"ready"`
	TransportReady    bool   `json:"transport_ready"`
	PolicyReady       bool   `json:"policy_ready"`
	PolicyError       string `json:"policy_error"`
	ForeignIPVerified bool   `json:"foreign_ip_verified"`
	GeoCountry        string `json:"geo_country"`
	GeoCheck          string `json:"geo_check"`
	GeoError          string `json:"geo_error"`
	geoKey            geoCacheKey
	checkedPolicy     geoCacheKey
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	Paused            bool     `json:"paused"`
	EffectiveEnabled  bool     `json:"effective_enabled"`
	PublicListening   bool     `json:"public_listening"`
	Pid               int      `json:"pid"`
	Listen            string   `json:"listen"`
	Port              int      `json:"port"`
	Endpoint          string   `json:"endpoint"`
	Outer             string   `json:"outer"`
	ManualEP          bool     `json:"manual_endpoint"`
	ExitInfo          string   `json:"exit_info"`
	ExitNode          string   `json:"exit_node"`
	ExitCountry       string   `json:"exit_country"`      // /meta public-IP country, not node geography
	ExitNodeCountry   string   `json:"exit_node_country"` // /meta colo.cca2 (observed HTTP colo)
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

	PolicyMode            string `json:"policy_mode"`
	PolicyCountry         string `json:"policy_country"`
	PolicyCountryVerified bool   `json:"policy_country_verified"`
}

type exitParse struct {
	info, node, country, nodeCountry string
}

var _ = time.Now
