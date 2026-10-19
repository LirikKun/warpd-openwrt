# warpd

[Русский](README.ru.md)

A WARP proxy manager for OpenWrt with a built-in web UI. Create SOCKS profiles, find working endpoints and check connection status without changing the router's network setup.

## Features

- Multiple WARP / AmneziaWG (AWG), MASQUE QUIC/H2 and Relay-WARP profiles.
- Compact TUI-style web console: Proxies, Relay, Search and Settings; drafts survive navigation.
- Explicit Rename, separate ports, endpoint settings and enable switches for each profile.
- Per-profile geography modes: off, exclusions or a strict selected public-IP country.
- Automatic endpoint discovery, manual endpoint pins and availability checks.
- Relay activation/IP monitoring, with optional linked tunnel profiles.
- English and Russian UI; no external web services needed to load the panel.

warpd does not change routes, firewall, router DNS or SSClash settings, and does not redirect all traffic automatically. Bundled Mihomo and WarpScout use a separate warpd directory; existing shared binaries are not overwritten.

## Install and upgrade

Requires **OpenWrt arm64 or amd64**, root SSH access, working HTTPS and enough free storage/RAM to unpack the bundle. Connect to your router as root (replace the example address):

```sh
ssh root@192.168.10.1
```

Run the **same command for a new installation or an upgrade**:

```sh
wget -T 30 -qO- https://github.com/LirikKun/warpd-openwrt/raw/refs/heads/main/install-warpd.sh | ash
```

The installer detects the architecture, checks requirements and verifies the downloaded bundle's SHA-256. It asks before installing missing dependencies through your configured `apk`/`opkg` repositories and offers **English / Русский** for the UI.

Upgrades preserve configuration, accounts, profiles and the current UI language unless you change it. Proxy connections are briefly interrupted. **A routine upgrade does not need a new account.**

Before upgrading, keep a private backup off the router of these files, if present:

- `/etc/config/warpd` — settings.
- `/etc/warpd/warp-account.json` — shared WARP account.
- `/etc/warpd/profiles.json` — profiles and Relay credentials.

Older profile settings migrate on first start. Downgrading the binary alone does not undo that migration. [Release downloads and notes](https://github.com/LirikKun/warpd-openwrt/releases/tag/v3.1.0).

## First start

1. Open `http://<router-LAN-IP>:9095/` in your browser.
2. A fresh installation has **one WARP profile on port 4001**, but no account yet.
3. Open **Settings → Diagnostics and log → Account maintenance · destructive actions → New account**. Confirm and wait for successful registration in the log.
4. If endpoint discovery has not started, open **Search → Find for all**. Wait until the profile is ready before connecting clients.
5. Use **Add proxy** for more profiles; each has its own port. Existing installations retain their profiles and ports.

Registration and discovery require external connectivity. If a profile is not ready, its SOCKS port stays closed; check the status and log in the panel.

### Geography policy

Under each profile's **Advanced** settings, **off** bypasses geography only; **exclusions** rejects excluded observed nodes, node countries and public-IP countries; **strict** also requires the selected public-IP country, verified by fresh independent geolocation and an agreeing current `/meta` result. Unknown required or conflicting evidence blocks access. Transport and credential checks still apply in every mode. Checks are periodic, not instant country-change detection. Existing profiles without an explicit mode keep their compatibility behavior until changed. The Relay server country is not the public-IP target.

## Connect a client

Set your application's SOCKS5 proxy to the router's LAN address and the profile's port. For the initial WARP profile, use `router-LAN-IP:4001`.

Prefer **SOCKS hostname resolution** (`socks5h`) so the proxy resolves destination names. For example, from a computer with curl:

```sh
curl --proxy socks5h://192.168.10.1:4001 --noproxy '' \
  --connect-timeout 12 --max-time 25 https://ifconfig.co/json
```

SOCKS supports **TCP CONNECT only, not UDP or BIND**. MASQUE QUIC support does not add UDP support for SOCKS clients. Restarting or reconfiguring the shared engine can briefly affect other profiles.

## Relay setup

1. Under **Relay → Add Relay**, save a **disabled** resource with your private activation URL. Before enabling, open **Advanced**, configure the intended source IP/interface, check the outgoing public IP and acknowledge that activation and tunnel traffic use the same path. Saving only the link does not configure that path automatically. Do not publish the URL or open it from another machine.
2. Add a **disabled Relay-WARP** profile linked to that resource; choose its server country and SOCKS port. The server country does not guarantee the exit country.
3. In the profile's **Tunnel account · this profile only** section, enter its assigned credentials manually or explicitly confirm **Register a new account**. These credentials are separate from the shared WARP account.
4. Enable the **parent Relay resource first, then the child profile**. Enabling the parent can send activation requests to the provider. Wait until the child is ready; saving settings alone does not mean the tunnel works.
5. Connect using `socks5h://<router-LAN-IP>:<profile-port>`, passing hostnames rather than locally resolved addresses.
6. To stop both, **disable the child before the parent**. Turning off only the parent pauses its children but retains their saved enabled choices; child switches are blocked while the parent is off.

Relay availability depends on your provider and outgoing connection. A successful activation response is not a ready tunnel. [Detailed Relay reference](docs/relay-runtime-integration.md).

## Access and private data

**Use only on a trusted LAN.** By default, management listens on `0.0.0.0:9095` without an API token, and SOCKS has no client authentication. No WAN ports are opened, but you must keep these services inaccessible from the Internet and untrusted networks.

You can restrict management with `web_listen` and set `warpd.global.web_secret` for API authentication; restart after changing these settings externally. **An API token does not protect SOCKS.** Keep account files, profile stores, activation URLs and backups private.

## Uninstall

Remove warpd while **keeping settings, accounts and profiles**:

```sh
warpd-uninstall
```

To also delete saved settings and credentials:

```sh
warpd-uninstall --purge
```

`--purge` deletes exactly these saved data files: `/etc/config/warpd`, `/etc/warpd/warp-account.json` and `/etc/warpd/profiles.json`. Back up first if needed. External shared binaries, SSClash and shared system packages are not removed.

## Credits

Thanks to [llimonix](https://github.com/nellimonix) for the external Relay service. warpd uses its own client implementation; no code from the service's project is included.

## Help and references

Start with **Settings → Diagnostics and log** in the panel. Over SSH, use `logread -e warpd` or `/etc/init.d/warpd status`. Check the account, selected ports, router time and Internet access before changing other services.

- [v3.1.0 release notes](docs/releases/v3.1.0.md)
- [Profile API reference](docs/profile-api-contract.md) · [Relay reference](docs/relay-runtime-integration.md)
- [Third-party components and licenses](THIRD_PARTY.md). No license has been chosen for warpd's own code.
