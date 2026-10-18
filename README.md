# warpd

[Русский](https://github.com/LirikKun/warpd-openwrt/blob/main/README.ru.md)

A small WARP proxy manager for OpenWrt, with endpoint discovery, independent service switches, availability checks and exit-geography reporting. Choose an English or Russian web UI during installation.

One Mihomo process serves the enabled SOCKS5 proxies; WarpScout runs only for discovery and account registration. No TUN, routes, firewall rules or transparent traffic redirection are added. SSClash is not required.

> “Enabled” is a setting, not proof that a proxy works. An acceptable HTTP colo does not guarantee a foreign public IP. Unsuitable exits are flagged in the UI, but the policy assessment itself does not block traffic through a reachable SOCKS proxy.

## Features

- WARP / AmneziaWG, MASQUE (QUIC), MASQUE H2 and a MASQUE → WARP chain.
- Independent SOCKS-port switches, persisted in UCI.
- Internal chain dependencies kept running even when standalone WARP/MASQUE is disabled.
- One shared proxy engine; turning every service off stops it but leaves the panel available.
- Automatic discovery, manually pinned endpoints and rate-limited rescans.
- Separate transport, exit-policy and independent M2W public-IP verification statuses.
- Embedded web UI without CDNs, external fonts or Node.js on the router.

## Requirements

- OpenWrt with procd and UCI, on Linux `arm64` or `amd64`. The main tested platform is OpenWrt 25.12.5, `mediatek/filogic`, `aarch64_cortex-a53`.
- Root SSH access, free ports and enough overlay and `/tmp` space to unpack the bundle. On OpenWrt, `/tmp` uses RAM. Mihomo is the largest component; the installer checks available space.
- A system CA trust store (`ca-bundle` or equivalent) and correct date/time for HTTPS. The bootstrap can install missing supported packages after consent, using existing repositories; it never edits feeds.
- Connectivity to Cloudflare for registration/tunnels and to HTTPS health-check services.
- For the initial script download below: `wget` with working HTTPS certificate verification. The bootstrap requires `curl` for release assets (offered through the same package-consent flow if missing), with a 180-second total deadline per attempt and at most three attempts per asset. Archive extraction requires `tar` with gzip support. Never disable TLS verification to work around a download error.
- The installer needs `/bin/sh`, `sha256sum`, `cat`, `cp`, `mv`, `chmod`, `mkdir`, `dirname`, `uname`, `id`, `readlink`, `grep`, `wc`, `ls`, `df`, `awk`, `mktemp`, `rm`, `rmdir`, `ln`, `sleep` (usually BusyBox applets), plus `uci` on the router. File sizes are checked with `wc -c`; rollback-directory permissions with `ls -nd` and `awk`. The bootstrap also checks `date`, `tar`, `gzip`, `procd`, `ubus`, `jsonfilter`, procd helpers and the CA bundle. Missing supported packages are offered through `apk`/`opkg`; missing foundational firmware utilities must be restored explicitly.

The bundle includes warpd and pinned versions of WarpScout 0.16.0 and Mihomo 1.19.31. Third-party binaries go into a warpd-specific directory, not over SSClash binaries. Sources and licenses: [THIRD_PARTY.md](THIRD_PARTY.md).

## Installation

### 1. Run the installer on the router

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

### 2. First start

Open `http://<router-LAN-IP>:9095/`. Only WARP is enabled by default; the other services start disabled.

A fresh installation has no account. Open **Diagnostics and log** → **Account maintenance · destructive actions** → **New account**, or in the Russian UI **«Диагностика и журнал» (Diagnostics and log)** → **«Обслуживание аккаунта · опасные действия» (Account maintenance · destructive actions)** → **«Новый аккаунт» (New account)**. Confirm and wait for successful registration in the log. Then click **«Найти для всех» (Find for all)** if discovery has not started automatically. Registration and discovery need external connectivity; an installed service is not yet a working tunnel.

Do not create a new account during a normal upgrade: this replaces shared credentials and may interrupt every service's connections.

### Access and security

Initially, the panel and SOCKS listeners bind to `0.0.0.0`, and the API token is empty. This is intended for a trusted LAN with an appropriate existing firewall policy, **not** safe Internet exposure. The installer does not open WAN ports or change the firewall. On an untrusted LAN, restrict the bind addresses or set `warpd.global.web_secret` through UCI before use; restart warpd after changing the address, port or token.

Do not put tokens in URLs or publish `/etc/warpd/warp-account.json` or the active `/tmp/warpd-mihomo.yaml`. Cross-site request protection is no substitute for authentication and a trusted network.

## Upgrading

Run the bootstrap from the new release in the same way, or download the new bundle and run its inner `install.sh`. Existing UCI settings and the account are preserved. If using external dependencies, repeat `--mihomo`/`--warpscout` explicitly to keep that layout; without these flags, dependencies from the new bundle are installed.

Upgrading a running service briefly interrupts its proxy connections. Do not assume a previously disabled service will be enabled by an upgrade. Manual service controls:

```sh
/etc/init.d/warpd start
/etc/init.d/warpd stop
/etc/init.d/warpd restart
/etc/init.d/warpd enable
/etc/init.d/warpd disable
```

Before upgrading, save private copies of `/etc/config/warpd` and `/etc/warpd/warp-account.json` off the router. They contain settings and keys; do not commit them to Git.

## Uninstalling

Remove the application and its own dependencies, keeping settings and the account:

```sh
warpd-uninstall
```

Also remove the UCI configuration and the known account file:

```sh
warpd-uninstall --purge
```

`--purge` deletes account keys: back them up first if you may need them. Unknown user files must not be recursively removed with the account directory. External `/usr/bin/warpscout`, `/opt/clash/bin/clash`, SSClash and network settings are outside the uninstaller's scope.

The installed uninstaller disappears after removal. Use `sh uninstall.sh` from an extracted bundle for an idempotent check or repeat removal.

## Ports and client setup

| Service | Default SOCKS5 port | Fresh installation |
| --- | --- | --- |
| WARP / AWG | 4001 | enabled |
| MASQUE / QUIC | 4003 | disabled |
| MASQUE H2 | 4004 | disabled |
| MASQUE → WARP (M2W) | 4005 | disabled |
| Panel/API | 9095 | available while the service runs |

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
| `/etc/config/warpd` | persistent UCI settings |
| `/etc/warpd/warp-account.json` | shared private account |
| `/tmp/warpd-mihomo.yaml` | generated configuration containing keys, in RAM |
| `/var/run/warpd`, `/var/warpd` | working directories, in RAM on OpenWrt |

Legacy manual installations without `/usr/libexec/warpd/` continue to use `/opt/clash/bin/clash` and `/usr/bin/warpscout`. If a warpd-specific installation is damaged, warpd does not silently fall back to a shared binary.

## UCI and API

Main options live in `warpd.global`: `enabled`, `check_period`, `scan_sample`, `scan_jobs`, `autoscan_on_start`, `max_ping`, `exclude_nodes`, `exclude_countries`, `fail_restart`, `fail_rescan`, `dme_rescan_gap`, `web_listen`, `web_port`, `web_secret`, `web_language` (`en` or `ru`). Exclusions are comma-separated lists. WARP excludes DME and RU by default; the scanner's country describes the HTTP colo, not necessarily the public IP.

`warpd.warp`, `warpd.masque`, `warpd.masqueh2` and `warpd.m2w` contain `enabled`, `listen`, `port` and manual endpoints. M2W uses `outer_endpoint` and `inner_endpoint`. An empty manual endpoint means automatic selection. UI changes apply immediately; after editing UCI externally, run `/etc/init.d/warpd restart`.

Current status:

```sh
curl -fsS http://192.168.10.1:9095/api/status
```

Enable or disable one service:

```sh
curl -fsS -H 'Content-Type: application/json' \
  -d '{"proxy":"masque","enabled":false}' \
  http://192.168.10.1:9095/api/proxy
```

Proxy keys: `warp`, `masque`, `masqueh2`, `m2w`. If a token is configured, add an `Authorization: Bearer <token>` header; never publish a real token in examples. HTTP 200 on a toggle means the setting was saved and its application verified. Errors or pending application return non-2xx; the desired state may already have been saved. Read `/api/status` again rather than assuming an error means rollback.

## Limitations and diagnostics

- A foreign HTTP colo and the public-IP country are different observations. M2W requires independent confirmation of a foreign public IP; other services assess the observed node's suitability.
- In testing, MASQUE/H2 often reached DME, and a foreign M2W IP was not confirmed. The project does not promise a foreign IP or a way around every restriction.
- Transport availability is not exit-policy suitability. An unsuitable but reachable SOCKS proxy can still carry traffic; disable it if you do not want to use it.
- All services share one engine. Restarting it drops their connections. Disabling one service should leave the others on, but applying configuration can briefly affect active tunnels.
- Port conflicts, missing CA certificates or account, incorrect time and unavailable external networks are separate failure causes, not reasons to modify SSClash or the firewall automatically.

Start with **«Диагностика и журнал»** and the discovery log in the panel, then check `logread -e warpd`, `/etc/init.d/warpd status` and `/api/status`. Never publish keys or raw active configurations.

## Building and testing

A Linux computer needs Python 3 and Go 1.24+, plus HTTPS access to GitHub for the initial dependency download. No root, Docker, OpenWrt SDK or host `apk` installation is required:

```sh
git clone https://github.com/LirikKun/warpd-openwrt.git
cd warpd-openwrt
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

UI DOM tests: [ui_interaction.README.md](ui_interaction.README.md). Node.js/jsdom are development-only dependencies, not router requirements.

To check actual installed files without starting services, use a separate offline root:

```sh
cd /path/to/extracted/warpd-2.2.0-linux-arm64
mkdir -p /absolute/path/to/empty-directory
sh install.sh --root /absolute/path/to/empty-directory
sh uninstall.sh --root /absolute/path/to/empty-directory --purge
```

This does not test procd or working tunnels. Never use a live filesystem or its system directories as the test root.

## Licenses

No license has been chosen for warpd's own code. Third-party licenses and obligations still apply: see [THIRD_PARTY.md](THIRD_PARTY.md) and the `licenses/` files inside the installation archive.
