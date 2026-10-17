#!/bin/sh
# Portable OpenWrt installer; no package manager changes or network operations.
# SHA256SUMS detects corruption, not publisher authenticity.
set -eu
BUNDLE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
fail() { printf 'warpd install: %s\n' "$*" >&2; exit 1; }
ROOT=
OFFLINE=0
MIHOMO=
WARPSCOUT=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --root|--mihomo|--warpscout)
            [ "$#" -ge 2 ] || fail "Missing value for $1"
            case "$2" in /*) ;; *) fail "Expected absolute path for $1" ;; esac
            case "$1" in
                --root) [ "$OFFLINE" = 0 ] || fail 'Duplicate --root'; ROOT=${2%/}; OFFLINE=1 ;;
                --mihomo) [ -z "$MIHOMO" ] || fail 'Duplicate --mihomo'; MIHOMO=$2 ;;
                --warpscout) [ -z "$WARPSCOUT" ] || fail 'Duplicate --warpscout'; WARPSCOUT=$2 ;;
            esac
            shift 2 ;;
        *) fail "Unknown argument: $1" ;;
    esac
done
if [ "$OFFLINE" = 1 ]; then
    case "$ROOT" in *[!/]*) ;; *) fail '--root must not refer to /' ;; esac
    [ -n "$ROOT" ] && [ -d "$ROOT" ] || fail '--root must be an existing non-root directory'
else
    [ -f /etc/openwrt_release ] || fail 'OpenWrt required (use --root for offline staging)'
    [ "$(id -u)" = 0 ] || fail 'Root privileges required'
fi
for tool in sha256sum cat cp mv chmod mkdir dirname uname id readlink grep wc ls df awk mktemp rm rmdir ln sleep; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing prerequisite: $tool"
done
if [ "$OFFLINE" = 0 ]; then
    command -v uci >/dev/null 2>&1 || fail 'Missing prerequisite: uci'
fi
ARCH=$(cat "$BUNDLE/ARCH")
case "$ARCH" in arm64|amd64) ;; *) fail 'Unsupported ARCH' ;; esac
if [ "$OFFLINE" = 0 ]; then
    case "$(uname -m):$ARCH" in aarch64:arm64|arm64:arm64|x86_64:amd64|amd64:amd64) ;; *) fail 'Architecture mismatch' ;; esac
fi
for external in "$MIHOMO" "$WARPSCOUT"; do
    [ -n "$external" ] || continue
    [ -f "$external" ] && [ -x "$external" ] || fail "Dependency is not executable: $external"
    resolved=$(readlink -f "$external") || fail 'Cannot resolve external dependency'
    for path in "$external" "$resolved"; do
        case "$path" in
            "$ROOT/usr/libexec/warpd/"*|"$ROOT/usr/share/warpd/"*|"$ROOT/usr/bin/warpd"|"$ROOT/etc/init.d/warpd"|"$ROOT/usr/sbin/warpd-uninstall"|"$ROOT/etc/config/warpd")
                fail "Dependency points at an installer-owned path: $external" ;;
        esac
    done
done
# Inspect every ancestor without following links. Refuse special files too.
safe_dir() {
    check=$1
    while [ "$check" != / ] && [ -n "$check" ]; do
        [ ! -L "$check" ] || fail "Symlink directory refused: $check"
        [ ! -e "$check" ] || [ -d "$check" ] || fail "Not a directory: $check"
        check=${check%/*}
    done
}
case "$ROOT" in *'
'*|*'|'*|*'/../'*|*'/./'*|*/..|*/.) fail 'Unsafe root path' ;; esac
for directory in /usr/bin /usr/libexec/warpd /etc/init.d /etc/rc.d /usr/sbin /usr/share/warpd /etc/config /etc/warpd; do
    safe_dir "$ROOT$directory"
done
EXPECTED='install.sh uninstall.sh VERSION ARCH bin/warpd bin/warpscout bin/mihomo files/warpd.init files/warpd.config README.md THIRD_PARTY.md licenses/warpscout-MIT.txt licenses/mihomo-GPL-3.0.txt'
SEEN=' '
COUNT=0
[ -f "$BUNDLE/SHA256SUMS" ] && [ ! -L "$BUNDLE/SHA256SUMS" ] || fail 'Missing regular SHA256SUMS'
while read -r hash name extra; do
    [ -z "$extra" ] && [ "${#hash}" = 64 ] || fail 'Malformed SHA256SUMS'
    case "$hash" in *[!0123456789abcdefABCDEF]*) fail 'Invalid checksum' ;; esac
    name=${name#\*}
    name=${name#./}
    case " $EXPECTED " in *" $name "*) ;; *) fail "Unexpected checksum path: $name" ;; esac
    case "$SEEN" in *" $name "*) fail "Duplicate checksum: $name" ;; esac
    safe_dir "$(dirname "$BUNDLE/$name")"
    [ -f "$BUNDLE/$name" ] && [ ! -L "$BUNDLE/$name" ] || fail "Missing regular payload: $name"
    SEEN="$SEEN$name "
    COUNT=$((COUNT + 1))
done < "$BUNDLE/SHA256SUMS"
[ "$COUNT" = 13 ] || fail 'Incomplete SHA256SUMS'
(cd "$BUNDLE" && sha256sum -c SHA256SUMS) || fail 'Checksum verification failed'
# Package database queries are read-only; never mix portable and packaged warpd.
if [ "$OFFLINE" = 0 ]; then
    if command -v apk >/dev/null 2>&1 && apk info -e warpd >/dev/null 2>&1; then
        fail 'warpd is package-managed by apk; remove that package explicitly before portable installation'
    fi
    if command -v opkg >/dev/null 2>&1 && opkg status warpd 2>/dev/null | grep -q '^Status: .* installed'; then
        fail 'warpd is package-managed by opkg; remove that package explicitly before portable installation'
    fi
fi
PORTABLE=0
if [ -e "$ROOT/usr/share/warpd/OWNER" ]; then
    [ ! -L "$ROOT/usr/share/warpd/OWNER" ] && [ "$(cat "$ROOT/usr/share/warpd/OWNER")" = warpd-portable-v1 ] || fail 'Unrecognized portable ownership marker'
    PORTABLE=1
fi
if [ "$PORTABLE" = 0 ]; then
    for target in /usr/libexec/warpd/mihomo /usr/libexec/warpd/warpscout /usr/sbin/warpd-uninstall /usr/share/warpd/VERSION /usr/share/warpd/ARCH; do
        [ ! -e "$ROOT$target" ] && [ ! -L "$ROOT$target" ] || fail "Refusing to overwrite unowned private file: $target"
    done
fi
# Conservative peak-space budget: all new files plus all old backups, metadata,
# filesystem block rounding and 256 KiB reserve. Check each destination mount.
TARGETS='/usr/bin/warpd /usr/libexec/warpd/mihomo /usr/libexec/warpd/warpscout /etc/init.d/warpd /usr/sbin/warpd-uninstall /usr/share/warpd/VERSION /usr/share/warpd/ARCH /usr/share/warpd/OWNER /etc/config/warpd'
NEEDED=262144
for source in bin/warpd bin/mihomo bin/warpscout files/warpd.init uninstall.sh VERSION ARCH files/warpd.config; do
    case "$source" in
        bin/mihomo) [ -z "$MIHOMO" ] || continue ;;
        bin/warpscout) [ -z "$WARPSCOUT" ] || continue ;;
    esac
    size=$(wc -c < "$BUNDLE/$source")
    NEEDED=$((NEEDED + size + 4096))
done
for target in $TARGETS; do
    dest="$ROOT$target"
    case "$target" in
        /usr/libexec/warpd/mihomo|/usr/libexec/warpd/warpscout) ;;
        *) [ ! -L "$dest" ] || fail "Symlink target refused: $dest" ;;
    esac
    [ ! -e "$dest" ] || [ -f "$dest" ] || fail "Not a regular target: $dest"
    if [ -e "$dest" ] || [ -L "$dest" ]; then
        if [ -L "$dest" ]; then
            # Back up the link, not its external binary; include readlink's newline.
            size=$(readlink "$dest" | wc -c)
        else
            size=$(wc -c < "$dest")
        fi
        NEEDED=$((NEEDED + size + 4096))
    fi
done
if [ -f "$ROOT/etc/config/warpd" ]; then
    # Existing config is staged byte-for-byte, not replaced by small defaults.
    size=$(wc -c < "$ROOT/etc/config/warpd")
    NEEDED=$((NEEDED + size + 4096))
fi
for target in $TARGETS; do
    probe="$ROOT${target%/*}"
    while [ ! -d "$probe" ]; do probe=${probe%/*}; done
    [ -w "$probe" ] || fail "Destination not writable: $probe"
    available=$(df -Pk "$probe" | awk 'NR > 1 {n=$4} END {print n}')
    case "$available" in ''|*[!0-9]*) fail "Cannot determine free space: $probe" ;; esac
    [ "$((available * 1024))" -ge "$NEEDED" ] || fail "Insufficient disk space at $probe (need $NEEDED bytes including rollback backups)"
done
UPGRADE=0
WAS_RUNNING=0
SERVICE_CHANGED=0
BOOTLINKS=
if [ "$OFFLINE" = 0 ]; then
    for link in /etc/rc.d/[SK][0-9][0-9]warpd; do
        [ -e "$link" ] || [ -L "$link" ] || continue
        [ -L "$link" ] || fail "Unexpected boot entry: $link"
        value=$(readlink "$link")
        case "$value" in ../init.d/warpd|/etc/init.d/warpd) ;; *) fail "Unowned boot link: $link" ;; esac
        BOOTLINKS="$link|$value
$BOOTLINKS"
    done
fi
[ ! -e "$ROOT/usr/bin/warpd" ] && [ ! -e "$ROOT/etc/init.d/warpd" ] || UPGRADE=1
if [ "$OFFLINE" = 0 ] && [ -e /etc/init.d/warpd ]; then
    [ -x /etc/init.d/warpd ] || fail 'Existing init script is not executable'
    grep -q '^PROG=/usr/bin/warpd$' /etc/init.d/warpd || fail 'Existing init script is not recognized as warpd-owned'
    /etc/init.d/warpd running >/dev/null 2>&1 && WAS_RUNNING=1
fi
if [ "$OFFLINE" = 0 ]; then
    for entry in /proc/[0-9]*/exe; do
        executable=$(readlink "$entry" 2>/dev/null || :)
        case "$executable" in
            /usr/bin/warpd|'/usr/bin/warpd (deleted)')
                [ -x /etc/init.d/warpd ] || fail 'Running warpd has no recognized init service; stop it explicitly before installing'
                WAS_RUNNING=1 ;;
        esac
    done
fi
# Never signal by executable name: stop only this init service, then wait for
# its exact daemon PIDs and their pre-stop descendants (including scanners).
collect_tree() {
    for child in $(cat /proc/"$1"/task/[0-9]*/children 2>/dev/null); do collect_tree "$child"; done
    PIDS="$PIDS $1"
}
stop_wait() {
    PIDS=
    for entry in /proc/[0-9]*/exe; do
        executable=$(readlink "$entry" 2>/dev/null || :)
        case "$executable" in
            /usr/bin/warpd|'/usr/bin/warpd (deleted)')
                pid=${entry#/proc/}; pid=${pid%/exe}; collect_tree "$pid" ;;
        esac
    done
    /etc/init.d/warpd stop || return 1
    attempts=0
    while :; do
        alive=0
        /etc/init.d/warpd running >/dev/null 2>&1 && alive=1
        for pid in $PIDS; do
            if kill -0 "$pid" 2>/dev/null && ! grep -q '^State:.*Z' "/proc/$pid/status" 2>/dev/null; then alive=1; fi
        done
        [ "$alive" = 1 ] || return 0
        [ "$attempts" -lt 15 ] || return 1
        sleep 1
        attempts=$((attempts + 1))
    done
}
wait_running() {
    attempts=0
    stable=0
    while [ "$attempts" -lt 10 ]; do
        if /etc/init.d/warpd running >/dev/null 2>&1; then stable=$((stable + 1)); else stable=0; fi
        [ "$stable" -lt 2 ] || return 0
        sleep 1
        attempts=$((attempts + 1))
    done
    return 1
}
# Each replacement is staged beside its destination (also on split mounts).
# Backups stay there until service verification succeeds; traps restore touched files.
umask 077
JOURNAL=
DIRS=
SUCCESS=0
ACCOUNT_MODE=
if [ -d "$ROOT/etc/warpd" ]; then
    # Minimal OpenWrt may lack stat. POSIX ls exposes all mode bits, including
    # uppercase S/T (special bit set without execute permission).
    ACCOUNT_MODE=$(LC_ALL=C ls -nd "$ROOT/etc/warpd" | awk '
        {
            special = 0
            if (substr($1, 4, 1) ~ /[sS]/) special += 4
            if (substr($1, 7, 1) ~ /[sS]/) special += 2
            if (substr($1, 10, 1) ~ /[tT]/) special += 1
            mode = ""
            for (i = 2; i <= 8; i += 3) {
                bits = (substr($1, i, 1) == "r" ? 4 : 0)
                bits += (substr($1, i + 1, 1) == "w" ? 2 : 0)
                bits += (substr($1, i + 2, 1) ~ /[xst]/ ? 1 : 0)
                mode = mode bits
            }
            printf "%d%s\n", special, mode
        }
        END { if (NR != 1) exit 1 }
    ')
fi
ensure_dir() {
    [ -d "$1" ] && return 0
    ensure_dir "${1%/*}"
    mkdir "$1"
    DIRS="$1
$DIRS"
    chmod 755 "$1"
}
finish() {
    status=$?
    trap - 0 HUP INT TERM
    set +e
    RECOVERY=0
    if [ "$SUCCESS" != 1 ] && [ "$SERVICE_CHANGED" = 1 ] && [ -x /etc/init.d/warpd ]; then
        if ! stop_wait; then
            printf '%s\n' 'ROLLBACK BLOCKED: service did not stop; same-directory .warpd-stage backups retained for recovery.' >&2
            exit 1
        fi
    fi
    while IFS='|' read -r stage dest; do
        [ -n "$stage" ] || continue
        if [ "$SUCCESS" != 1 ] && [ -f "$stage/committed" ]; then
            if [ -e "$stage/old" ] || [ -L "$stage/old" ]; then
                if ! mv -f "$stage/old" "$dest"; then
                    printf 'ROLLBACK FAILED: %s; original retained at %s/old\n' "$dest" "$stage" >&2
                    RECOVERY=1
                    continue
                fi
            else
                rm -f "$dest"
            fi
        fi
        rm -f "$stage/new" "$stage/old" "$stage/committed"
        rmdir "$stage"
    done <<EOF
$JOURNAL
EOF
    if [ "$RECOVERY" = 1 ]; then
        [ -z "$ACCOUNT_MODE" ] || chmod "$ACCOUNT_MODE" "$ROOT/etc/warpd"
        printf '%s\n' 'Incomplete rollback; service left stopped. Restore retained original backups before retrying.' >&2
        exit 1
    fi
    if [ "$SUCCESS" != 1 ]; then
        if [ "$OFFLINE" = 0 ] && [ "$SERVICE_CHANGED" = 1 ] && [ "$UPGRADE" = 0 ]; then
            for link in /etc/rc.d/[SK][0-9][0-9]warpd; do
                [ ! -L "$link" ] || rm -f "$link"
            done
            while IFS='|' read -r link value; do
                [ -z "$link" ] || ln -s "$value" "$link"
            done <<EOF
$BOOTLINKS
EOF
        fi
        if [ "$SERVICE_CHANGED" = 1 ] && [ "$WAS_RUNNING" = 1 ]; then
            /etc/init.d/warpd start && wait_running || printf '%s\n' 'ROLLBACK WARNING: old files restored but old service could not be restarted; inspect logread -e warpd.' >&2
        fi
        [ -z "$ACCOUNT_MODE" ] || chmod "$ACCOUNT_MODE" "$ROOT/etc/warpd"
        while IFS= read -r directory; do
            [ -z "$directory" ] || rmdir "$directory"
        done <<EOF
$DIRS
EOF
        printf '%s\n' 'Installation failed; previous files restored.' >&2
        [ "$status" != 0 ] || status=1
    fi
    exit "$status"
}
trap finish 0
trap 'exit 1' HUP INT TERM
stage_file() {
    source=$1
    dest="$ROOT$2"
    mode=$3
    external=${4-}
    case "$dest" in
        "$ROOT/usr/libexec/warpd/mihomo"|"$ROOT/usr/libexec/warpd/warpscout") ;;
        *) [ ! -L "$dest" ] || fail "Symlink target refused: $dest" ;;
    esac
    [ ! -e "$dest" ] || [ -f "$dest" ] || fail "Not a regular target: $dest"
    ensure_dir "${dest%/*}"
    stage=$(mktemp -d "${dest%/*}/.warpd-stage.XXXXXX")
    JOURNAL="$stage|$dest
$JOURNAL"
    if [ -e "$dest" ] || [ -L "$dest" ]; then
        cp -Pp "$dest" "$stage/old"
    fi
    if [ -n "$external" ]; then
        ln -s "$external" "$stage/new"
    else
        cp "$source" "$stage/new"
        chmod "$mode" "$stage/new"
    fi
}
stage_file "$BUNDLE/bin/warpd" /usr/bin/warpd 755
stage_file "$BUNDLE/bin/mihomo" /usr/libexec/warpd/mihomo 755 "$MIHOMO"
stage_file "$BUNDLE/bin/warpscout" /usr/libexec/warpd/warpscout 755 "$WARPSCOUT"
stage_file "$BUNDLE/files/warpd.init" /etc/init.d/warpd 755
stage_file "$BUNDLE/uninstall.sh" /usr/sbin/warpd-uninstall 755
stage_file "$BUNDLE/VERSION" /usr/share/warpd/VERSION 644
stage_file "$BUNDLE/ARCH" /usr/share/warpd/ARCH 644
# A fixed marker authorizes only the explicitly listed portable files.
stage_file "$BUNDLE/VERSION" /usr/share/warpd/OWNER 644
printf '%s\n' 'warpd-portable-v1' > "$stage/new"
if [ -e "$ROOT/etc/config/warpd" ]; then
    stage_file "$ROOT/etc/config/warpd" /etc/config/warpd 600
else
    stage_file "$BUNDLE/files/warpd.config" /etc/config/warpd 600
fi
ensure_dir "$ROOT/etc/warpd"
if [ "$OFFLINE" = 0 ] && [ "$WAS_RUNNING" = 1 ]; then
    SERVICE_CHANGED=1
    stop_wait || fail 'Timed out waiting for warpd and its children to stop; no app files replaced'
fi
while IFS='|' read -r stage dest; do
    [ -n "$stage" ] || continue
    # Mark before rename so a signal between the two still restores safely.
    : > "$stage/committed"
    mv -f "$stage/new" "$dest"
done <<EOF
$JOURNAL
EOF
chmod 700 "$ROOT/etc/warpd"
if [ "$OFFLINE" = 0 ]; then
    [ "$UPGRADE" = 1 ] || { SERVICE_CHANGED=1; /etc/init.d/warpd enable; }
    if { [ "$UPGRADE" = 0 ] || [ "$WAS_RUNNING" = 1 ]; } && [ "$(uci -q get warpd.global.enabled || :)" != 0 ]; then
        SERVICE_CHANGED=1
        /etc/init.d/warpd start || fail 'Service start failed; see logread -e warpd'
        wait_running || fail 'Service failed to remain running; see logread -e warpd'
    fi
fi
SUCCESS=1
if [ "$OFFLINE" = 1 ]; then
    printf '%s\n' 'Installed offline; service not started (host UCI/procd/processes untouched).'
else
    printf '%s\n' 'Installed warpd; service state verified.'
fi
