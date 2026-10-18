#!/bin/sh
# Only portable warpd paths are owned here; shared dependencies are never removed.
set -eu
fail() { printf 'warpd uninstall: %s\n' "$*" >&2; exit 1; }
ROOT=
OFFLINE=0
PURGE=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --root)
            [ "$#" -ge 2 ] || fail 'Missing --root value'
            [ "$OFFLINE" = 0 ] || fail 'Duplicate --root'
            case "$2" in /*) ;; *) fail '--root requires an absolute path' ;; esac
            ROOT=${2%/}; OFFLINE=1; shift 2 ;;
        --purge) [ "$PURGE" = 0 ] || fail 'Duplicate --purge'; PURGE=1; shift ;;
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
for tool in cat id readlink grep rm rmdir sleep; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing prerequisite: $tool"
done
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
OWNED=0
MARKER="$ROOT/usr/share/warpd/OWNER"
[ ! -L "$MARKER" ] || fail 'Symlink ownership marker refused'
if [ -f "$MARKER" ]; then
    [ "$(cat "$MARKER")" = warpd-portable-v1 ] || fail 'Unrecognized portable ownership marker'
    OWNED=1
fi
TARGETS='/usr/bin/warpd /usr/libexec/warpd/mihomo /usr/libexec/warpd/warpscout /etc/init.d/warpd /usr/sbin/warpd-uninstall /usr/share/warpd/VERSION /usr/share/warpd/ARCH /usr/share/warpd/OWNER'
for target in $TARGETS; do
    dest="$ROOT$target"
    [ ! -e "$dest" ] && [ ! -L "$dest" ] && continue
    [ "$OWNED" = 1 ] || fail 'Portable ownership marker missing; refusing to remove unowned application files'
    [ -f "$dest" ] || [ -L "$dest" ] || fail "Unexpected target type: $dest"
done
if [ "$PURGE" = 1 ]; then
    for target in /etc/config/warpd /etc/warpd/warp-account.json /etc/warpd/profiles.json; do
        dest="$ROOT$target"
        [ ! -L "$dest" ] || fail "Symlink purge target refused: $dest"
        [ ! -e "$dest" ] || [ -f "$dest" ] || fail "Unexpected purge target: $dest"
    done
fi
# Validate boot entries before init disable (rc.common may remove matching names).
if [ "$OWNED" = 1 ]; then
    for link in "$ROOT"/etc/rc.d/[SK][0-9][0-9]warpd; do
        [ -e "$link" ] || [ -L "$link" ] || continue
        [ -L "$link" ] || fail "Unexpected boot entry: $link"
        case "$(readlink "$link")" in ../init.d/warpd|/etc/init.d/warpd) ;; *) fail "Unowned boot link: $link" ;; esac
    done
fi
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
if [ "$OFFLINE" = 0 ] && [ ! -x /etc/init.d/warpd ]; then
    for entry in /proc/[0-9]*/exe; do
        executable=$(readlink "$entry" 2>/dev/null || :)
        case "$executable" in
            /usr/bin/warpd|'/usr/bin/warpd (deleted)') fail 'Running warpd has no usable init service; stop it explicitly before uninstalling' ;;
        esac
    done
fi
if [ "$OWNED" = 1 ] && [ "$OFFLINE" = 0 ] && [ -e /etc/init.d/warpd ]; then
    [ ! -L /etc/init.d/warpd ] && grep -q '^PROG=/usr/bin/warpd$' /etc/init.d/warpd || fail 'Unrecognized init script; refusing service operations'
    stop_wait || fail 'Service or its children did not stop; application files retained'
    /etc/init.d/warpd disable || fail 'Could not disable warpd; application files retained'
fi
if [ "$OWNED" = 1 ]; then
    for link in "$ROOT"/etc/rc.d/[SK][0-9][0-9]warpd; do
        [ -L "$link" ] || continue
        case "$(readlink "$link")" in ../init.d/warpd|/etc/init.d/warpd) rm -f "$link" ;; esac
    done
    for target in $TARGETS; do rm -f "$ROOT$target"; done
    # No recursive deletion: unknown sentinel files keep these directories alive.
    rmdir "$ROOT/usr/libexec/warpd" "$ROOT/usr/share/warpd" 2>/dev/null || :
fi
if [ "$PURGE" = 1 ]; then
    printf '%s\n' 'WARNING: --purge deletes /etc/config/warpd (configuration), /etc/warpd/warp-account.json (WARP account), and /etc/warpd/profiles.json (profiles and Relay activation credentials); unknown files are preserved.' >&2
    rm -f "$ROOT/etc/config/warpd" "$ROOT/etc/warpd/warp-account.json" "$ROOT/etc/warpd/profiles.json"
    rmdir "$ROOT/etc/warpd" 2>/dev/null || :
else
    printf '%s\n' 'Configuration, WARP account, profiles and Relay activation credentials preserved (use --purge to delete the known files).'
fi
if [ "$OFFLINE" = 1 ]; then
    printf '%s\n' 'Uninstalled warpd offline; host service not touched.'
else
    printf '%s\n' 'Uninstalled warpd.'
fi
