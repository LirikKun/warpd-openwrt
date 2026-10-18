# Dynamic profiles implementation contract (working specification)

Implementation workers share this contract; not a shipped-feature claim.

## Go model / ownership

Core worker owns config.go, profile_store.go and matching tests; all existing orchestrator.go/checks.go/state.go/geo_policy.go/mihomo.go/main.go and runtime tests as needed. Parent owns server.go and new profile_api.go + tests, installer/build/docs. UI worker owns webui.html, webui.go, UI/i18n tests. Relay worker owns independent new relay_client.go/relay_monitor.go and tests; integration is parent-owned.

Extend ProxySettings with Name, Type, Transport, RelayID, RelayCountry, ExcludeNodes, ExcludeCountries strings, AWG AWGOptions. Existing Key is stable ID, Endpoint/Listen/Port/Enabled stay. JSON profile API is explicit DTO, not capitalized Go JSON. Type: warp|masque|relay-warp; legacy-m2w can be retained migration-only to preserve existing chain, never newly creatable. Transport: quic|http2 (MASQUE only). RelayCountry: DE|NL|FI|LV|EE. Profile type/name must not be conflated with ID. Preserve old manually constructed test fixtures via type fallback from legacy keys.

RelaySettings: Key,Name string; Enabled bool; ActivationURL string (SECRET, never API serialized); SourceIP,Interface string; PathConfirmed bool (explicit same-source path acknowledgement); MonitorInterval int seconds. Core can add safe needed config fields, but report additions. Secret must be persisted only in private profile-store file, never exposed as status or error. Relays map in Settings.

Settings adds ProfilePath string, ProfileSchema int, Revision uint64, Relays map[string]*RelaySettings. Snapshot deep-copies all new fields. Path defaults /etc/warpd/profiles.json; tests inject a temp path, never write /etc. Main calls cfg.InitProfiles(defaultProfilePath) exactly once before runtime; init is idempotent. Failed/corrupt/unknown schema must not reset data or overwrite file. Keep management available with visible configuration error if possible; never silently load fresh defaults over damaged config.

Core exports:
- const defaultProfilePath = "/etc/warpd/profiles.json"
- func (s *Settings) InitProfiles(path string) error
- func (s *Settings) ChangeProfiles(edit func(*Settings) error) error (snapshot -> edit -> validate -> atomic private persistence -> publish; parent holds orch.applyMu). Do not publish failed persistence. Increment revision only on success. edit may add/delete profiles/relays.
- func NewProxyProfile(id, kind string) *ProxySettings (defaults including persisted AWG; caller sets name/port/options)
- func profileType(id string,p *ProxySettings) string
- func profileIDs(g Settings) []string (deterministic)
- func effectiveEnabled(g Settings,p *ProxySettings) bool (parent enabled + child flag)
- func effectiveExclusions(g Settings,p *ProxySettings) (nodes,countries string)
- AWGOptions struct with JSON fields preset,jc,jmin,jmax,s1,s2,h1,h2,h3,h4,i1; core chooses validated numerical types. DefaultAWGOptions() AWGOptions; validation strict, structured, no arbitrary YAML.

Core validator enforces IDs, types/transports, ports/binds/controller/web collisions, known Relay countries, references, dependency-safe deletion, secret URL validation (use relay worker API if available), profile-specific fields and AWG values. Immutable profile types enforced in API and validator where practical. No new chains.

Global settings remain UCI. Existing ApplyConfigPatch must not replace dynamic maps with four fixed defaults; old fixed-slot mutating endpoints must be rejected or safely route new model, not silently persist unused UCI values. New global exclusions affect effective rules of every profile, union local/global; strict normalized codes; return unknown-geography errors rather than assume allowed. Legacy deny-RU safety stays unless explicitly redesigned, never silently drop previous effective exclusions.

## API (parent-owned)

GET /api/profiles -> {revision,profiles:[...],relays:[...]}
POST /api/profiles -> create. JSON fields name,type,transport,enabled,listen,port,endpoint,relay_id,relay_country,exclude_nodes,exclude_countries,awg. Omitted values use model defaults; server creates unpredictable stable ID. Return 201 {id,persisted:true,applied:boolean,error?:safe_string}; persistence and engine application distinguished.
PATCH /api/profiles/{id} -> partial update (name, enabled, bind/port, pin, filters, AWG; type immutable). DELETE /api/profiles/{id}.
POST /api/relays -> name,url,enabled,source_ip,interface,path_confirmed,monitor_interval. Omitted enabled defaults false, interval 300. PATCH /api/relays/{id}, DELETE /api/relays/{id}. Empty/replaced URL must never bypass dependent-profile constraint. Parent disable preserves child Enabled; UI pauses child toggles while parent disabled; deletion remains possible.
POST /api/relays/{id}/activate -> explicit request; requires enabled and confirmed source path, bounded/coalesced; response request/result not tunnel-ready claim.
POST /api/scan {for:<profile-id>|both} remains usable for each dynamic profile. POST /api/restart {proxy:both} remains shared engine restart with disruptive confirmation; no fake per-profile restart.

Every mutation retains bearer auth, origin/fetch-site checks, application/json requirement, bounded strict JSON decode and unknown-field rejection. Serialize CRUD/dependency validation, scanner applies and runtime applies with orch.applyMu. Rejected validation no writes. If desired state persists but apply fails, return 503 with persisted:true,applied:false and readback new desired values.

Public profile DTO fields: id,name,type,transport,enabled,effective_enabled,listen,port,endpoint,relay_id,relay_country,exclude_nodes,exclude_countries,effective_exclude_nodes,effective_exclude_countries,awg. Legacy chain DTO can add outer,inner with migration-only label. Public Relay DTO: id,name,enabled,configured,source_ip,interface,path_confirmed,monitor_interval,linked_profiles count plus safe runtime activation status; never url.

GET /api/status retains config, scan, mihomo, proxies object keyed by profile ID. Parent appends profiles array, relays array, revision from same snapshot. Existing ProxyState gains ID/type/name/paused metadata as needed; Ready remains fresh applied health + policy, not enabled. Status never serializes complete Settings/RelaySettings/store directly.

## Runtime and acceptance

Multiple same-type profiles need distinct endpoints, health and configuration. One shared Mihomo process. Cancel/discard stale scans/probes on edits/delete/parent disable, including non-rendered filter revisions. Monitor-only Relay must not load WARP account or launch engine. Retained legacy chains remain migration-only, with safety behavior preserved.

Do not call red readiness data-plane enforcement. For strict exclusion enforcement, implement a real private probe/public listener gate or equivalent; test actual listener state. No forbidden public traffic while an unverified candidate is tested. Parent/UI must report any remaining enforcement limitation, never mark this criterion completed without proof.

All tests local synthetic inputs, no live account registrations or personal activation requests. Record behavioral RED/GREEN logs under scratch, not repo. Avoid concurrent edits of other workers' files. Do not commit/deploy; parent integrates, runs full suite/review and handles publication.
