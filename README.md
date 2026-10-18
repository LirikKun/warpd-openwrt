# warpd

[Русский](https://github.com/LirikKun/warpd-openwrt/blob/main/README.ru.md)

A small WARP proxy manager for OpenWrt, with endpoint discovery, independent service switches, availability checks and exit-geography reporting. The web UI is currently in Russian.

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
- A system CA trust store (`ca-bundle` or equivalent) and correct date/time for HTTPS. The installer neither changes repositories nor installs system packages automatically.
- Connectivity to Cloudflare for registration/tunnels and to HTTPS health-check services.
- For the download procedure below: `wget` with working HTTPS certificate verification and `tar` with gzip support. Never disable TLS verification to work around a download error.
- The installer needs `/bin/sh`, `sha256sum`, `cat`, `cp`, `mv`, `chmod`, `mkdir`, `dirname`, `uname`, `id`, `readlink`, `grep`, `wc`, `ls`, `df`, `awk`, `mktemp`, `rm`, `rmdir`, `ln`, `sleep` (usually BusyBox applets), plus `uci` on the router. File sizes are checked with `wc -c`; rollback-directory permissions with `ls -nd` and `awk`. No extra packages are installed.

The bundle includes warpd and pinned versions of WarpScout 0.16.0 and Mihomo 1.19.31. Third-party binaries go into a warpd-specific directory, not over SSClash binaries. Sources and licenses: [THIRD_PARTY.md](THIRD_PARTY.md).

## Installation

### 1. Download, verify and install on the router

The public [v2.1.1 release](https://github.com/LirikKun/warpd-openwrt/releases/tag/v2.1.1) provides both `linux-arm64` and `linux-amd64` bundles. No GitHub account, CLI or token is needed. This online README is the current installation guide; the existing, unchanged 2.1.1 archives still contain the older Russian README from before the repository became public. No new release is needed to follow these instructions.

Connect as root, replacing the example LAN address:

```sh
ssh root@192.168.10.1
```

Run this whole block in the router's SSH shell. It chooses the router architecture, downloads into a fresh private directory, checks SHA-256 **before extraction**, and stops on error. It does not stream downloaded code into a shell.

```sh
(
  set -e
  [ "$(id -u)" = 0 ] || { printf '%s\n' 'Run this as root on the router.' >&2; exit 1; }
  case "$(uname -m)" in
    aarch64|arm64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) printf '%s\n' 'Unsupported architecture: arm64 or amd64 required.' >&2; exit 1 ;;
  esac
  version=2.1.1
  umask 077; workdir=$(mktemp -d /tmp/warpd-install.XXXXXX) && cd "$workdir" || exit 1
  printf 'Temporary directory: %s\n' "$workdir"
  archive="warpd-${version}-linux-${arch}.tar.gz"
  base="https://github.com/LirikKun/warpd-openwrt/releases/download/v${version}"
  wget -O "$archive" "$base/$archive"
  wget -O "$archive.sha256" "$base/$archive.sha256"
  sha256sum -c "$archive.sha256"
  tar -xzf "$archive"
  rm -f "$archive"
  cd "warpd-${version}-linux-${arch}"
  sh install.sh
  printf 'Installation finished. Temporary directory to remove manually: %s\n' "$workdir"
)
```

The compressed archive is removed after extraction to reduce RAM use. The subshell leaves your original shell directory unchanged; its `workdir` variable does not survive. After a successful installation, manually remove **only the exact temporary directory printed above** (including its extracted bundle and checksum), after confirming the path. On failure, use the earlier “Temporary directory” line to inspect the files before deciding whether to remove them. Do not run recursive cleanup against an unset or guessed variable.

SHA-256 detects corruption, not publisher authenticity: obtain both the archive and checksum from the trusted HTTPS release. The installer checks the bundle before changing installed files, installs a procd service and enables autostart on a fresh install. This is a portable bundle, not an APK/IPK package: it is not registered with `apk`/`opkg` and uses the uninstaller below.

**Optional: reuse external dependencies.** If compatible binaries are already installed and you want to avoid a second installed copy, replace the final `sh install.sh` line in the block above with this command **before running the block**. This is an alternative, not a second installation step:

```sh
sh install.sh \
  --mihomo /opt/clash/bin/clash \
  --warpscout /usr/bin/warpscout
```

warpd creates its own links to these executables; it neither overwrites nor removes them. A later replacement by their owner also affects warpd. Older Clash builds are not necessarily compatible: MASQUE, AmneziaWG, `dialer-proxy` and the corresponding Mihomo capabilities are required.

**Fallback: download on a computer.** Download the router's architecture-specific archive and its `.sha256` from the same release and run `sha256sum -c warpd-2.1.1-linux-arm64.tar.gz.sha256` there (substitute `amd64` for an x86-64 router). On the router, create a private directory with `umask 077; mktemp -d /tmp/warpd-install.XXXXXX` and note the printed path. Copy **both** files into that exact directory with `scp -O`; for example, `scp -O warpd-2.1.1-linux-arm64.tar.gz warpd-2.1.1-linux-arm64.tar.gz.sha256 root@192.168.10.1:/tmp/warpd-install.REPLACE/`, replacing the destination with the real printed path. `-O` supports Dropbear devices without SFTP. In a router subshell with `set -e`, change into that directory, repeat the checksum check, then extract, remove the compressed archive, enter the extracted directory and run the chosen installer command as above. Do not extract if verification fails. Clean up only the exact staging directory after success.

### 2. First start

Open `http://<router-LAN-IP>:9095/`. Only WARP is enabled by default; the other services start disabled.

A fresh installation has no account. In the Russian UI, open **«Диагностика и журнал» (Diagnostics and log)** → **«Обслуживание аккаунта · опасные действия» (Account maintenance · dangerous actions)** → **«Новый аккаунт» (New account)**. Confirm and wait for successful registration in the log. Then click **«Найти для всех» (Find for all)** if discovery has not started automatically. Registration and discovery need external connectivity; an installed service is not yet a working tunnel.

Do not create a new account during a normal upgrade: this replaces shared credentials and may interrupt every service's connections.

### Access and security

Initially, the panel and SOCKS listeners bind to `0.0.0.0`, and the API token is empty. This is intended for a trusted LAN with an appropriate existing firewall policy, **not** safe Internet exposure. The installer does not open WAN ports or change the firewall. On an untrusted LAN, restrict the bind addresses or set `warpd.global.web_secret` through UCI before use; restart warpd after changing the address, port or token.

Do not put tokens in URLs or publish `/etc/warpd/warp-account.json` or the active `/tmp/warpd-mihomo.yaml`. Cross-site request protection is no substitute for authentication and a trusted network.

## Upgrading

Download the new bundle and run its `install.sh` in the same way. Existing UCI settings and the account are preserved. If using external dependencies, repeat `--mihomo`/`--warpscout` explicitly to keep that layout; without these flags, dependencies from the new bundle are installed.

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

Main options live in `warpd.global`: `enabled`, `check_period`, `scan_sample`, `scan_jobs`, `autoscan_on_start`, `max_ping`, `exclude_nodes`, `exclude_countries`, `fail_restart`, `fail_rescan`, `dme_rescan_gap`, `web_listen`, `web_port`, `web_secret`. Exclusions are comma-separated lists. WARP excludes DME and RU by default; the scanner's country describes the HTTP colo, not necessarily the public IP.

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
cd /path/to/extracted/warpd-2.1.1-linux-arm64
mkdir -p /absolute/path/to/empty-directory
sh install.sh --root /absolute/path/to/empty-directory
sh uninstall.sh --root /absolute/path/to/empty-directory --purge
```

This does not test procd or working tunnels. Never use a live filesystem or its system directories as the test root.

## Licenses

No license has been chosen for warpd's own code. Third-party licenses and obligations still apply: see [THIRD_PARTY.md](THIRD_PARTY.md) and the `licenses/` files inside the installation archive.
