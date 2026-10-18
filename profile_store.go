package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
)

const defaultProfilePath = "/etc/warpd/profiles.json"
const profileSchema = 1
const maxProfileBytes = 1 << 20

// RelaySettings contains a capability. Never serialize it in an HTTP response.
type RelaySettings struct {
	Key, Name           string
	Enabled             bool
	ActivationURL       string `json:"activation_url"`
	SourceIP, Interface string
	PathConfirmed       bool
	MonitorInterval     int
}
type AWGOptions struct {
	Preset string `json:"preset"`
	JC     int    `json:"jc"`
	JMin   int    `json:"jmin"`
	JMax   int    `json:"jmax"`
	S1     int    `json:"s1"`
	S2     int    `json:"s2"`
	H1     uint32 `json:"h1"`
	H2     uint32 `json:"h2"`
	H3     uint32 `json:"h3"`
	H4     uint32 `json:"h4"`
	I1     string `json:"i1"`
}

const defaultAWGI1 = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"

func DefaultAWGOptions() AWGOptions {
	return AWGOptions{Preset: "warpscout-0.16.0-mihomo-1.19.31-awg-v1", JC: 6, JMin: 10, JMax: 50, H1: 1, H2: 2, H3: 3, H4: 4, I1: defaultAWGI1}
}
func NewProxyProfile(id, kind string) *ProxySettings {
	p := (&ProxySettings{}).defaults(id)
	p.Type = kind
	p.Name = id
	p.AWG = DefaultAWGOptions()
	if kind == "masque" {
		p.Transport = "quic"
		p.Protocol = "masque"
	}
	return p
}
func profileType(id string, p *ProxySettings) string {
	if p != nil && p.Type != "" {
		return p.Type
	}
	switch id {
	case "warp":
		return "warp"
	case "masque", "masqueh2":
		return "masque"
	case "m2w":
		return "legacy-m2w"
	}
	return ""
}
func profileIDs(g Settings) []string {
	ids := make([]string, 0, len(g.Proxies))
	for id := range g.Proxies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func effectiveEnabled(g Settings, p *ProxySettings) bool {
	if p == nil || !p.Enabled {
		return false
	}
	if p.RelayID != "" {
		r := g.Relays[p.RelayID]
		return r != nil && r.Enabled
	}
	return true
}

type profileDocument struct {
	Schema   int                       `json:"schema"`
	Revision uint64                    `json:"revision"`
	Profiles map[string]*ProxySettings `json:"profiles"`
	Relays   map[string]*RelaySettings `json:"relays"`
}

// InitProfiles is one-way initialization. Existing empty maps are authoritative.
func (s *Settings) InitProfiles(path string) error {
	lock := s.profileMutex()
	lock.Lock()
	defer lock.Unlock()
	if s.Snapshot().ProfileSchema != 0 {
		return nil
	}
	if path == "" {
		path = defaultProfilePath
	}
	g := s.Snapshot()
	g.ProfilePath = path
	dir, err := openProfileStoreDir(path)
	if err != nil {
		return err
	}
	defer dir.file.Close()
	f, err := dir.open(dir.name)
	if err == nil {
		defer f.Close()
		b, err := boundedProfileRead(f)
		if err != nil {
			return err
		}
		var d profileDocument
		if decodePrivateProfileDocument(b, &d) != nil || d.Schema != profileSchema || d.Profiles == nil {
			return fmt.Errorf("profile store is invalid or has an unsupported schema")
		}
		g.Proxies = d.Profiles
		g.Relays = d.Relays
		g.Revision = d.Revision
		g.ProfileSchema = d.Schema
		if err := validateProfiles(g, nil); err != nil {
			return fmt.Errorf("profile store validation failed: %w", err)
		}
		if dir.file.Chmod(0700) != nil || f.Chmod(0600) != nil {
			return fmt.Errorf("profile store permissions could not be secured")
		}
	} else if os.IsNotExist(err) {
		if s.legacyPresent != nil {
			for id := range g.Proxies {
				if !g.legacyPresent[id] || g.legacyFresh && id != "warp" {
					delete(g.Proxies, id)
				}
			}
		}
		if len(g.Proxies) == 0 {
			p := NewProxyProfile("warp", "warp")
			p.Enabled = true
			g.Proxies = map[string]*ProxySettings{"warp": p}
		}
		for id, p := range g.Proxies {
			p.Type = profileType(id, p)
			p.Name = defstr(p.Name, id)
			if p.Type == "masque" {
				if id == "masqueh2" {
					p.Transport = "http2"
				} else {
					p.Transport = "quic"
				}
			}
			if p.AWG.Preset == "" {
				p.AWG = DefaultAWGOptions()
				if p.Type != "masque" {
					// Legacy renderer used the account's captured packet, NOT the
					// scanner's gen_i1 protocol label. Preserve that actual value.
					var captured warpAccount
					af, err := dir.open("warp-account.json")
					if err == nil {
						var raw []byte
						raw, err = boundedProfileRead(af)
						af.Close()
						if err == nil {
							err = json.Unmarshal(raw, &captured)
						}
					}
					if err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("legacy account cannot be read for AWG migration")
					}
					if captured.GenI1 != "" {
						p.AWG.I1 = captured.GenI1
					} else if p.GenI1 != "" {
						p.AWG.I1 = p.GenI1
					}
				}
			}
		}
		if g.Relays == nil {
			g.Relays = map[string]*RelaySettings{}
		}
		g.ProfileSchema = profileSchema
		g.Revision = 1
		if err := validateProfiles(g, nil); err != nil {
			return fmt.Errorf("legacy profile migration requires attention: %w", err)
		}
		if err := persistProfilesInDir(g, dir, nil); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("profile store could not be read")
	}
	mu := s.mutex()
	mu.Lock()
	defer mu.Unlock()
	s.Proxies = g.Proxies
	s.Relays = g.Relays
	s.ProfilePath = path
	s.ProfileSchema = g.ProfileSchema
	s.Revision = g.Revision
	return nil
}

// Caller serializes this transaction with runtime application through applyMu.
func (s *Settings) ChangeProfiles(edit func(*Settings) error) error {
	lock := s.profileMutex()
	lock.Lock()
	defer lock.Unlock()
	before := s.Snapshot()
	g := before.Snapshot()
	if g.ProfileSchema != profileSchema || g.ProfilePath == "" {
		return fmt.Errorf("profile store is not initialized")
	}
	if err := edit(&g); err != nil {
		return err
	}
	g.ProfilePath = before.ProfilePath
	g.ProfileSchema = before.ProfileSchema
	g.Revision = before.Revision
	if err := validateProfiles(g, &before); err != nil {
		return err
	}
	g = g.Snapshot() // callbacks must never retain aliases into published state
	g.Revision++
	if err := persistProfiles(g, before); err != nil {
		return err
	}
	mu := s.mutex()
	mu.Lock()
	defer mu.Unlock()
	s.Proxies = g.Proxies
	s.Relays = g.Relays
	s.Revision = g.Revision
	return nil
}
func persistProfiles(g, before Settings) error {
	dir, err := openProfileStoreDir(g.ProfilePath)
	if err != nil {
		return err
	}
	defer dir.file.Close()
	return persistProfilesInDir(g, dir, &before)
}
func persistProfilesInDir(g Settings, dir *profileStoreDir, before *Settings) error {
	b, err := json.MarshalIndent(profileDocument{profileSchema, g.Revision, g.Proxies, g.Relays}, "", "  ")
	if err != nil {
		return fmt.Errorf("profile store encoding failed")
	}
	if len(b) > maxProfileBytes {
		return fmt.Errorf("profile store exceeds size limit")
	}
	return dir.write(b, before)
}

// Compare the supported document's meaning, not its indentation or key order.
// Unknown fields fail closed so an edit cannot silently discard unsupported data,
// using the same exact private decoder as initialization. No document or
// credential-bearing identity is included in errors or public settings.
func checkCurrentProfileDocument(b []byte, before Settings) error {
	var d profileDocument
	if decodePrivateProfileDocument(b, &d) != nil || d.Schema != profileSchema || d.Profiles == nil {
		return fmt.Errorf("profile store is invalid or has an unsupported schema")
	}
	current := before.Snapshot()
	current.Proxies, current.Relays = d.Profiles, d.Relays
	if validateProfiles(current, nil) != nil {
		return fmt.Errorf("profile store validation failed")
	}
	current = current.Snapshot() // omitted/null Relay maps mean an empty map
	if d.Revision != before.Revision || !reflect.DeepEqual(current.Proxies, before.Proxies) || !reflect.DeepEqual(current.Relays, before.Relays) {
		return fmt.Errorf("profile store changed; reload before editing")
	}
	return nil
}
