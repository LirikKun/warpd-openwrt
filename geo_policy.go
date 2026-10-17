package main

import (
	"context"
	"strings"
	"time"
)

// No credentials or rendered configuration are retained in the policy cache.
type geoCacheKey struct {
	generation                       uint64
	endpoint, outer, listen          string
	port                             int
	excludedNodes, excludedCountries string
}

func geoKeyFor(g Settings, generation uint64, ep, outer string, p *ProxySettings) geoCacheKey {
	return geoCacheKey{generation, ep, outer, p.Listen, p.Port, g.ExcludeNodes, g.ExcludeCountries}
}
func geoEligible(g Settings, res exitParse) bool {
	return observedNodePolicyError(g, res.node, res.nodeCountry) == "" && knownCountry(res.country) && strings.ToUpper(strings.TrimSpace(res.country)) != "RU"
}
func knownCountry(country string) bool {
	country = strings.ToUpper(strings.TrimSpace(country))
	return country == "RU" || observedNodePolicyError(Settings{}, "GEO", country) == ""
}
func geoFresh(st ProxyState, key geoCacheKey, now time.Time) bool {
	checked, err := time.Parse(time.RFC3339Nano, st.GeoCheck)
	return err == nil && st.geoKey == key && !checked.After(now) && now.Sub(checked) < 60*time.Second
}
func (c *ProxyCheck) clearGeo() {
	c.set(func(st *ProxyState) {
		st.GeoCountry = ""
		st.GeoCheck = ""
		st.GeoError = ""
		st.geoKey = geoCacheKey{}
	})
}
func (c *ProxyCheck) observeGeo(ctx context.Context, g Settings, res exitParse, key geoCacheKey) {
	if !geoEligible(g, res) {
		c.clearGeo()
		return
	}
	if geoFresh(c.snapshot(), key, time.Now()) {
		return
	}
	// Invalidate before renewal: a failed request must never reuse old success.
	c.clearGeo()
	result, err := c.geoProbe(ctx, key.listen, key.port)
	c.set(func(st *ProxyState) {
		st.geoKey = key
		st.GeoCountry = result.country
		st.GeoCheck = time.Now().Format(time.RFC3339Nano)
		if err != nil {
			st.GeoError = "independent geo verification failed"
			if reason, ok := err.(geoError); ok {
				st.GeoError = string(reason)
			}
		}
	})
}
func geoVerified(g Settings, st ProxyState, key geoCacheKey, now time.Time) bool {
	return geoFresh(st, key, now) && st.GeoError == "" && knownCountry(st.GeoCountry) && st.GeoCountry != "RU" &&
		geoEligible(g, exitParse{node: st.ExitNode, nodeCountry: st.ExitNodeCountry, country: st.ExitCountry})
}
