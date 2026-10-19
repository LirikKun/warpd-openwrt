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
	revision                         uint64
	policyMode, policyCountry        string
}

func geoKeyFor(g Settings, generation uint64, ep, outer string, p *ProxySettings) geoCacheKey {
	n, c := effectiveExclusions(g, p)
	listen, port := profileProbeAddress(g, p.Key, p)
	return geoCacheKey{generation, ep, outer, listen, port, n, c, g.Revision, p.PolicyMode, p.PolicyCountry}
}

func profileNeedsGeo(id string, p *ProxySettings) bool {
	return p != nil && (p.PolicyMode == "strict" || p.PolicyMode == "" && (profileType(id, p) == "legacy-m2w" || profileType(id, p) == "relay-warp"))
}

func publicCountryPolicyError(g Settings, country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if !knownCountry(country) {
		return "public IP country unknown"
	}
	for _, excluded := range strings.Split(g.ExcludeCountries, ",") {
		if strings.ToUpper(strings.TrimSpace(excluded)) == country {
			return "excluded public IP country " + country
		}
	}
	return ""
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
	eligible := geoEligible(g, res)
	if key.policyMode == "strict" {
		eligible = observedNodePolicyError(g, res.node, res.nodeCountry) == "" && publicCountryPolicyError(g, res.country) == "" && res.country == key.policyCountry
	}
	if !eligible {
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
	if key.policyMode == "strict" {
		g.ExcludeNodes, g.ExcludeCountries = key.excludedNodes, key.excludedCountries
		return geoFresh(st, key, now) && st.GeoError == "" && knownCountry(key.policyCountry) && st.GeoCountry == key.policyCountry &&
			st.ExitCountry == key.policyCountry && publicCountryPolicyError(g, st.GeoCountry) == "" && publicCountryPolicyError(g, st.ExitCountry) == "" && observedNodePolicyError(g, st.ExitNode, st.ExitNodeCountry) == ""
	}
	return geoFresh(st, key, now) && st.GeoError == "" && knownCountry(st.GeoCountry) && st.GeoCountry != "RU" &&
		geoEligible(g, exitParse{node: st.ExitNode, nodeCountry: st.ExitNodeCountry, country: st.ExitCountry})
}
