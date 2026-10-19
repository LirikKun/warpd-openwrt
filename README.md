# warpd

[Русский](README.ru.md)

A WARP proxy manager for OpenWrt with endpoint discovery, dynamic SOCKS profiles, availability checks and an English/Russian web UI. One shared Mihomo process serves tunnel profiles; WarpScout handles discovery and account registration. No TUN, routes, firewall rules or transparent redirection are added. SSClash is not required and its files are not replaced.

> **Version scope:** this README describes the **3.0.0 source candidate**, not a deployed or release-ready build. Offline review has passed; live acceptance remains outstanding. The download instructions below install the **previous published v2.2.0 release**, **not** the dynamic-profile model or TCP gates described here. No v3 release download is advertised.

> **Safety boundary:** enabled intent, engine availability, policy readiness and a public listener are separate states. Candidate public ports accept TCP SOCKS5 CONNECT only after fresh checks of the exact managed child, applied configuration/generation and observed exit policy. Relay-WARP additionally requires fresh independent foreign-egress verification. These checks do **not** establish provider binding or tunnel-peer location. The integrated Relay runtime has offline verification, not live provider/tunnel acceptance; there is no direct-WARP fallback.

## Features and candidate status

- Multiple WARP / AmneziaWG and MASQUE profiles, each with a stable ID, name, port, endpoint pin, health state and additive exclusions. MASQUE supports `quic` and `http2` (H2).
- Create, edit and delete profiles through the panel/API; type is immutable. Profiles and Relay resources are stored privately, not in four fixed UCI slots.
- Fresh candidate initialization creates **only one WARP profile**, enabled on port 4001. Other profiles must be added. An existing empty profile store stays empty, including monitor-only setups.
- Existing fixed-slot configurations migrate, including disabled entries. An existing MASQUE → WARP chain is retained as **`legacy-m2w`**, migration-only: rename, enable/disable or delete it; tunnel settings are read-only. **New chains cannot be created.**
- Automatic discovery, endpoint pins and rate-limited rescans. Candidate engine SOCKS listeners stay on private loopback ports while checked.
- App-owned public TCP gates close their listeners **and accepted streams** on revocation or authorization expiry. UDP ASSOCIATE and BIND are unsupported.
- Independent Relay IP monitoring and activation requests, without a WARP account or running engine in monitor-only mode. **Provider/tunnel compatibility remains unverified.**
- Embedded web UI without CDNs, external fonts or Node.js on the router. Without effective tunnel profiles, the engine can stop while management remains available.

Implementation references: [core integration](docs/core-integration.md), [API working contract](docs/profile-api-contract.md), [store](profile_store.go), [validation](profile_validation.go), [API](profile_api.go), [TCP gates](profile_gate.go). The working contract is not a shipped-feature guarantee.

## Requirements

- OpenWrt with procd and UCI, on Linux `arm64` or `amd64`. Source-candidate behavior still needs authorized router-side acceptance; local tests do not demonstrate a working tunnel.
- Root SSH access, free ports and enough overlay and `/tmp` space to unpack the bundle. On OpenWrt, `/tmp` uses RAM. Mihomo is the largest component; the installer checks available space.
- A system CA trust store (`ca-bundle` or equivalent) and correct date/time for HTTPS. The bootstrap can install missing supported packages after consent, using existing repositories; it never edits feeds.
- Connectivity to Cloudflare for registration/tunnels and to HTTPS health-check services.
- For the initial script download below: `wget` with working HTTPS certificate verification. The bootstrap requires `curl` for release assets (offered through the same package-consent flow if missing), with a 180-second total deadline per attempt and at most three attempts per asset. Archive extraction requires `tar` with gzip support. Never disable TLS verification to work around a download error.
- The installer needs `/bin/sh`, `sha256sum`, `cat`, `cp`, `mv`, `chmod`, `mkdir`, `dirname`, `uname`, `id`, `readlink`, `grep`, `wc`, `ls`, `df`, `awk`, `mktemp`, `rm`, `rmdir`, `ln`, `sleep` (usually BusyBox applets), plus `uci` on the router. File sizes are checked with `wc -c`; rollback-directory permissions with `ls -nd` and `awk`. The bootstrap also checks `date`, `tar`, `gzip`, `procd`, `ubus`, `jsonfilter`, procd helpers and the CA bundle. Missing supported packages are offered through `apk`/`opkg`; missing foundational firmware utilities must be restored explicitly.

The bundle includes warpd and pinned versions of WarpScout 0.16.0 and Mihomo 1.19.31. Third-party binaries go into a warpd-specific directory, not over SSClash binaries. Sources and licenses: [THIRD_PARTY.md](THIRD_PARTY.md).

## Installation — previous published v2.2.0 only

**This section installs v2.2.0, not the 3.0.0 source candidate.** v2.2.0 uses the earlier fixed-slot model and advisory exit-policy behavior; do not assume its public SOCKS ports have the candidate's TCP gate protection. Do not use these commands to downgrade a candidate installation.

### 1. Download and install the previous release

Connect over SSH as root, replacing the example address:

```sh
ssh root@192.168.10.1
```

Run this block. No GitHub account or token is needed. It saves the script to a private temporary file rather than piping downloaded code into a shell:

```sh
(
  umask 077
  script=$(mktemp /tmp/warpd-setup.XXXXXX) || exit 1
  wget -O "$script" https://github.com/LirikKun/warpd-openwrt/releases/download/v2.2.0/install-warpd.sh &&
    sh "$script"
  result=$?
  rm -f "$script"
  exit "$result"
)
```

The installer checks OpenWrt, root access, architecture (`arm64`/`amd64`), disk space, required commands and HTTPS CA certificates. It offers to install supported missing dependencies through your configured `apk` or `opkg` repositories. It does not upgrade the entire system, edit feeds, network, firewall or SSClash settings, or disable TLS verification. Missing foundational firmware components or a package manager unable to retrieve dependencies cause a clear failure instead of a partial application install.

During interactive setup, choose **English / Русский** for the web UI. Upgrades can keep the current language. The script then downloads the v2.2.0 bundle, verifies SHA-256 before extraction and runs the transactional installer, which rolls back application-file replacement or service-start failures. Temporary staging is cleaned automatically. Checksums detect corruption; they do not replace trust in the publisher and HTTPS.

For automation, save the script as above and replace the `sh "$script"` line with the appropriate invocation:

```sh
# Russian UI; allow installation of missing system packages:
sh "$script" --language ru --yes
# English UI:
sh "$script" --language en --yes
# Check requirements without installing anything:
sh "$script" --check-only
```

`--yes` consents to installing missing dependencies without another prompt. Without a terminal or this consent, missing packages are not installed. Shared system packages added by the package manager are not removed if the warpd installation fails or when warpd is uninstalled.

**External dependencies.** By default, the bundle installs its own Mihomo and WarpScout. To avoid a second installed copy when compatible binaries already exist, replace the invocation with this alternative rather than installing twice:

```sh
sh "$script" --language en \
  --mihomo /opt/clash/bin/clash \
  --warpscout /usr/bin/warpscout
```

warpd creates private links to these executables, without overwriting or removing them. Their owner's future updates also affect warpd. Older Clash builds may lack MASQUE, AmneziaWG or `dialer-proxy` support.

**Manual / offline installation.** The [v2.2.0 release](https://github.com/LirikKun/warpd-openwrt/releases/tag/v2.2.0) provides `linux-arm64` and `linux-amd64` archives and their `.sha256` files. Download the matching pair, run `sha256sum -c <archive-name>.sha256` **before extraction**, unpack into a private temporary directory and run `sh install.sh --language en` there. This inner installer does not download system packages: prepare requirements beforehand. Use `scp -O` when transferring from a computer to Dropbear without SFTP. Remove only your exact staging directory after success.

This is a portable bundle, not an APK/IPK package: warpd itself is not registered with `apk`/`opkg` and uses the uninstaller below. Fresh installs enable autostart; upgrades preserve settings, account and service state.

### 2. First start of the previous release

Open `http://<router-LAN-IP>:9095/`. In v2.2.0, only WARP is enabled by default; the other fixed services start disabled.

A fresh installation has no account. Open **Diagnostics and log** → **Account maintenance · destructive actions** → **New account**, or in the Russian UI **«Диагностика и журнал» (Diagnostics and log)** → **«Обслуживание аккаунта · опасные действия» (Account maintenance · destructive actions)** → **«Новый аккаунт» (New account)**. Confirm and wait for successful registration in the log. Then click **«Найти для всех» (Find for all)** if discovery has not started automatically. Registration and discovery need external connectivity; an installed service is not yet a working tunnel.

Do not create a new account during a normal upgrade: this replaces shared credentials and may interrupt every service's connections.

## Using the 3.0.0 source candidate

These are source-candidate semantics, **not instructions to deploy before review**. No published v3 installer is linked here. Build/test locally as described below.

### Profiles, migration and ports

The panel/API defaults to port 9095. Fresh initialization creates only WARP (ID `warp`, port 4001, enabled intent), not disabled MASQUE/H2/chain cards. Add profiles explicitly; new API-created profiles receive opaque stable IDs and an available port, starting the search at 4001. Names and types are not IDs. Ports must be distinct, including disabled profiles, and cannot collide with the panel or controller port 9099.

Without an existing store, UCI profile sections migrate without rewriting the original UCI configuration or account. Enabled flags, ports and endpoint pins are retained, including disabled entries. Migration preserves the actual legacy AWG I1 packet captured in the account; missing/invalid migration data causes a visible error rather than invented credentials or packets. An existing empty store is authoritative. Corrupt, invalid or unsupported stores are preserved and rejected: management reports `profile_error`, and profile runtime/scans remain blocked instead of silently resetting defaults.

Readiness after edits is refreshed by a configured health-check round, not an immediate tunnel probe inside CRUD. An enabled but unverified profile has **no public listener**; connection refusal can be the intended safety state.

### Access and private data

The default management bind and desired public profile bind are `0.0.0.0`; the API token is initially empty. This is for a trusted LAN with an appropriate existing firewall, **not Internet exposure**. No WAN ports are opened. Restrict management through UCI (`web_listen`) and profile binds through the profile API. Set `warpd.global.web_secret` for API authentication; it does **not** authenticate SOCKS clients. Restart after external changes to management bind/port/token. Cross-site request checks do not replace authentication or network access controls.

Never put tokens in URLs or publish the account, `/etc/warpd/profiles.json` or generated `/tmp/warpd-mihomo.yaml`. Relay activation URLs are credentials: write-only through the API, retained in the private store, absent from public DTOs and safe status/errors. Backups must remain private too.

### Relay resources, credentials and candidate runtime

Relay resources and child `relay-warp` profiles have separate enabled intent. Disabling a parent pauses its children without changing their saved enabled flags; child enable/disable requests are blocked while the parent is disabled. A child can still be deleted. Delete **all** linked profiles, including disabled ones, before deleting the parent. The same activation credential cannot belong to multiple resources. Leaving an existing URL field empty retains it; it cannot bypass dependencies.

Monitoring runs independently with an empty profile collection, without loading a WARP account or starting Mihomo. Resources start disabled. Monitoring/activation requires a source IP or interface and explicit same-source-path acknowledgement; requests are bounded/coalesced and subject to debounce, cooldown and retry limits. Enabling confirmed monitoring can cause provider requests; do not enable it merely to inspect settings.

Relay-WARP uses **profile-owned tunnel credentials**, separate from the activation resource and shared WARP account. Choose explicit, confirmed generation of a new account for that profile, or write-only manual entry of an existing private key, the **server's `[Peer] PublicKey`** (not the client's public key), assigned tunnel IPv4 and optional assigned IPv6, with compatible Reserved/DNS/MTU parameters as needed. Addresses are validated, not invented or borrowed from the shared account. Credential values are never returned by the public API. Saving or generating credentials stores them and revokes old authorization; it does **not** apply a tunnel or activate Relay. Save, enable, restart and monitoring never implicitly register an account.

Relay destination hostnames are resolved **inside the tunnel**, using the profile's explicit DNS address or, when absent, `1.1.1.1` and `1.0.0.1`. This avoids sending system-resolver Fake-IP answers into the tunnel; use SOCKS hostname resolution rather than supplying an already-resolved Fake-IP address. This policy does not change the router's DNS, SSClash, routes or firewall, and does not start a DNS listener. DNS reachability through a live provider remains part of authorized tunnel acceptance. An IPv6-only DNS server requires an assigned tunnel IPv6 address; the renderer never invents one.

The source candidate integrates a per-profile, loopback-only UDP bridge into the **same managed Mihomo child**, not a second engine or direct-WARP substitute. The bridge binds provider traffic and DNS to the confirmed source address/interface/family and authorizes only the exact child's UDP socket for the applied configuration/generation. Linux pidfd support, `/proc` socket ownership checks and permission to duplicate that child's socket descriptor are required; missing support or permission fails closed, without a weaker fallback. A public TCP gate still requires current bridge authorization, fresh tunnel health and exit policy, including independent foreign-egress verification. Credential storage, engine apply and public readiness are separate outcomes; read runtime readiness from `/api/status`, not account-action metadata.

**Provider/tunnel compatibility and live acceptance remain outstanding.** Offline fixtures and a target-kernel diagnostic do not establish a completed provider tunnel, binding or foreign exit; the candidate application has not been deployed for acceptance. A provider HTTP 200 is an unverified activation outcome, not confirmed binding or tunnel readiness. User acknowledgement and an HTTP IP observation alone do not prove that activation and tunnel traffic share the intended source path. Provider keys, assigned addresses, Reserved/AWG compatibility and actual path equivalence still require authorized live evidence. See [Relay runtime integration and evidence scope](docs/relay-runtime-integration.md).

### Filters and AWG compatibility

Effective exclusions are the **union** of built-in DME/RU, global UCI lists and per-profile lists. Local settings add restrictions; empty fields do not remove built-ins. Node codes are three-letter colo codes; country codes are validated. Unknown geography is not silently allowed. Endpoint pins do not bypass checks.

Persisted preset: `warpscout-0.16.0-mihomo-1.19.31-awg-v1`. Only **JC, Jmin, Jmax and I1** are editable (`jc`, `jmin`, `jmax`, `i1` in JSON), with bounded ranges and a validated I1 tag chain. **S1/S2 stay 0; H1–H4 stay 1,2,3,4**: pinned WarpScout cannot pass custom S/H values to its peer protocol. Incompatible edits are rejected; AWG customization does not apply to MASQUE. See [validation](profile_validation.go) for exact bounds. This is pinned-tool compatibility, not arbitrary AmneziaWG configuration support.

## Upgrading

Candidate installer behavior is documented from source, not release acceptance. It preserves UCI/global settings, account and profile store; first-run migration handles old sections. Pins, ports, saved enabled intent, UI language (unless explicitly overridden) and service/autostart state are retained. Empty stores are not repopulated. No router routes/firewall/network/SSClash changes or shared-binary replacement are performed.

Only use a reviewed matching bundle for an upgrade; the v2.2.0 download above is **not** a candidate upgrade path. Repeat `--mihomo`/`--warpscout` when deliberately keeping external dependencies; without these flags, the bundle installs its private copies.

Upgrading a running service briefly interrupts its proxy connections. Do not assume a previously disabled service will be enabled by an upgrade. Manual service controls:

```sh
/etc/init.d/warpd start
/etc/init.d/warpd stop
/etc/init.d/warpd restart
/etc/init.d/warpd enable
/etc/init.d/warpd disable
```

Before upgrading, save private copies of `/etc/config/warpd`, `/etc/warpd/warp-account.json` and `/etc/warpd/profiles.json` off the router. They contain settings and keys; do not commit them to Git.

## Uninstalling

The **candidate** uninstaller removes owned application files/private dependencies or links, keeping UCI, account, profile store and Relay credentials:

```sh
warpd-uninstall
```

Also delete the known private data files:

```sh
warpd-uninstall --purge
```

The candidate purge list is exactly **`/etc/config/warpd`, `/etc/warpd/warp-account.json`, `/etc/warpd/profiles.json`**. This deletes account keys and Relay credentials: back up first if needed. Unknown files are preserved; `/etc/warpd` is removed only if empty. External `/usr/bin/warpscout`, `/opt/clash/bin/clash`, SSClash, shared packages and network settings are outside scope. See [uninstaller source](packaging/uninstall.sh). The older v2.2.0 uninstaller predates the profile store; do not assume it has the candidate's purge behavior.

The installed uninstaller disappears after removal. Use `sh uninstall.sh` from an extracted bundle for an idempotent check or repeat removal.

## Ports and client setup

The candidate has only WARP on port 4001 after fresh initialization; management uses 9095. Additional profile ports are assigned or selected when created, not fixed by transport. Migrated profiles retain their existing ports. A public TCP port opens only after fresh readiness/policy verification. UDP ASSOCIATE and BIND are unsupported.

Configure SOCKS5 at `router-LAN-IP:4001` in your application or existing routing system. warpd does not route all router or client traffic through itself. For example, from a computer:

```sh
curl --proxy socks5h://192.168.10.1:4001 --noproxy '' \
  --connect-timeout 12 --max-time 25 https://ifconfig.co/json
```

`socks5h` passes the destination hostname to the SOCKS server. Do not use `--insecure`: TLS certificates must be verified.

## File locations

| Path | Purpose |
| --- | --- |
| `/usr/bin/warpd` | manager and embedded web UI |
| `/usr/libexec/warpd/mihomo` | owned engine or explicit link to an external binary |
| `/usr/libexec/warpd/warpscout` | owned scanner or explicit link to an external binary |
| `/etc/init.d/warpd` | procd service |
| `/usr/sbin/warpd-uninstall` | uninstaller |
| `/usr/share/warpd/` | installation metadata |
| `/etc/config/warpd` | global UCI settings and retained migration input |
| `/etc/warpd/profiles.json` | private atomic profile/Relay store, **0600**, parent **0700**; contains Relay credentials |
| `/etc/warpd/warp-account.json` | shared private account |
| `/tmp/warpd-mihomo.yaml` | generated configuration containing keys, in RAM |
| `/var/run/warpd`, `/var/warpd` | working directories, in RAM on OpenWrt |

Legacy manual installations without `/usr/libexec/warpd/` continue to use `/opt/clash/bin/clash` and `/usr/bin/warpscout`. If a warpd-specific installation is damaged, warpd does not silently fall back to a shared binary.

## UCI and API

Global options remain in `warpd.global`: `enabled`, `check_period`, `scan_sample`, `scan_jobs`, `autoscan_on_start`, `max_ping`, `exclude_nodes`, `exclude_countries`, `fail_restart`, `fail_rescan`, `dme_rescan_gap`, `web_listen`, `web_port`, `web_secret`, `web_language` (`en`/`ru`). Exclusions are comma-separated. Fresh seed `profile_schema=1` distinguishes the new layout. After initialization, use the panel or stable-ID API for profiles, not legacy UCI slots (`warpd.warp`, `warpd.masque`, `warpd.masqueh2`, `warpd.m2w`). External global UCI edits require a restart.

Read desired profiles and runtime separately:

```sh
curl -fsS http://192.168.10.1:9095/api/profiles
curl -fsS http://192.168.10.1:9095/api/status
```

Disable a profile using an actual `id` from `/api/profiles`, not its name/type. No credentials are included in this example:

```sh
profile_id='REPLACE_WITH_ID_FROM_API'
curl -fsS -X PATCH -H 'Content-Type: application/json' \
  -d '{"enabled":false}' \
  "http://192.168.10.1:9095/api/profiles/$profile_id"
```

With authentication configured, supply your bearer token privately in the `Authorization` header, never in a URL or published command. Profile/Relay CRUD requires strict bounded JSON and `application/json`; unknown fields are rejected. Routes: `POST /api/profiles`, `PATCH`/`DELETE /api/profiles/{id}`, `POST /api/relays`, `PATCH`/`DELETE /api/relays/{id}`; DELETE requests use `{}` JSON. Explicit activation is `POST /api/relays/{id}/activate` with `{}`. Scans use `POST /api/scan` with `{"for":"<profile-id>"}` or `{"for":"both"}`. Restart affects the shared engine, not one profile.

Legacy fixed-slot mutations (`/api/proxy`, `/api/endpoint`, `/api/endpoint/clear` and fixed-slot `/api/config` fields) are rejected in dynamic mode. Global UCI options remain separate. Responses distinguish **`persisted`** from **`applied`**; application failure can return HTTP 503 after desired state was saved. Even `applied:true` is not tunnel readiness. Read back `/api/profiles` and `/api/status`; do not assume non-2xx means rollback. Status exposes `profile_error`, per-ID transport/policy readiness, `public_listening` and effective enabled intent.

## Limitations and diagnostics

- Gates require **fresh transport health and observed HTTP-colo policy** for the exact managed child and current applied configuration/generation, endpoint and settings. A serving colo is **not proven tunnel-peer identity or a foreign public-IP guarantee**. Local tests do not prove live routing geography.
- Candidates are checked on loopback before public exposure. Unsuitable, stale or unverified candidates have no public TCP gate. Revocation/expiry closes existing forwarded streams and the listener. This is not a router-wide firewall and does not protect unrelated listeners or local access to candidate loopback ports.
- Public SOCKS supports **TCP CONNECT only**, not BIND or UDP ASSOCIATE. MASQUE QUIC as an internal transport does not imply UDP support for SOCKS clients.
- Relay-WARP and retained `legacy-m2w` additionally require independent fresh foreign-public-IP verification. No new chain feature, live foreign exit, successful Relay binding or provider tunnel is claimed.
- All tunnel profiles share one engine. Restart/reconfiguration can interrupt other profiles; readiness must be checked again. Monitor-only Relay remains independent.
- Port conflicts, missing CA/account, incorrect time, unavailable networks and store/migration errors are distinct causes, not reasons to modify SSClash, the firewall or network automatically.

Start with **Diagnostics and log** and the discovery log in the panel, then check `logread -e warpd`, `/etc/init.d/warpd status` and `/api/status`. Never publish keys or raw active configurations.

## Building and testing

Local development only until review/acceptance; these commands do not deploy or publish. Use the candidate checkout: cloning a published branch alone does not establish candidate provenance. A Linux computer needs Python 3 and Go 1.24+, plus HTTPS access to GitHub for the initial dependency download. No root, Docker, OpenWrt SDK or host `apk` installation is required:

```sh
# From the candidate checkout:
./build.sh --arch arm64
# For x86-64:
./build.sh --arch amd64
```

Set `GO=/absolute/path/to/go` if needed. Outputs are `dist/warpd-<version>-linux-<arch>.tar.gz` and `.sha256`. Dependencies are pinned by version and SHA-256; a corrupt cache is not trusted. `SOURCE_DATE_EPOCH` controls archive timestamps.

```sh
go test -race -count=1 ./...
go vet ./...
python3 -m unittest discover -s packaging -p '*_test.py'
python3 -m unittest discover -s tools -p 'test_*.py'
```

These are verification commands, not a claim that this README update ran the full suite or that local results establish release readiness.

UI DOM tests: [ui_interaction.README.md](ui_interaction.README.md). Node.js/jsdom are development-only dependencies, not router requirements.

To check actual installed files without starting services, use a separate offline root:

```sh
cd /path/to/extracted/candidate-bundle
mkdir -p /absolute/path/to/empty-directory
sh install.sh --root /absolute/path/to/empty-directory
sh uninstall.sh --root /absolute/path/to/empty-directory --purge
```

This does not test procd, provider activation or working tunnels. Archive names are not publication or acceptance evidence. Never use a live filesystem or its system directories as the test root.

## Licenses

No license has been chosen for warpd's own code. Third-party licenses and obligations still apply: see [THIRD_PARTY.md](THIRD_PARTY.md) and the `licenses/` files inside the installation archive.
