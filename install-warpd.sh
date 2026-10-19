#!/bin/sh
# Online OpenWrt bootstrap. Downloads only public, versioned warpd release assets.
set -eu
fail() { printf 'warpd bootstrap: %s\n' "$*" >&2; exit 1; }
# Call only with a sanitized expected value. Hash stdin on both sides, avoiding
# command-substitution NUL/newline loss and filename-dependent digest output.
canonical_line() { [ "$(sha256sum < "$1")" = "$(printf '%s\n' "$2" | sha256sum)" ]; }
CHECK=0
YES=0
VERSION=3.1.2
VERSION_SET=0
LANGUAGE=
MIHOMO=
WARPSCOUT=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --check-only) [ "$CHECK" = 0 ] || fail 'Duplicate --check-only'; CHECK=1; shift ;;
        --yes) [ "$YES" = 0 ] || fail 'Duplicate --yes'; YES=1; shift ;;
        --version|--language|--mihomo|--warpscout)
            [ "$#" -ge 2 ] || fail "Missing value for $1"
            case "$1" in
                --version) [ "$VERSION_SET" = 0 ] || fail 'Duplicate --version'; VERSION=$2; VERSION_SET=1 ;;
                --language) [ -z "$LANGUAGE" ] || fail 'Duplicate --language'; case "$2" in en|ru) LANGUAGE=$2 ;; *) fail 'Language must be en or ru' ;; esac ;;
                --mihomo|--warpscout)
                    case "$2" in /*) ;; *) fail "Expected absolute path for $1" ;; esac
                    case "$1" in
                        --mihomo) [ -z "$MIHOMO" ] || fail 'Duplicate --mihomo'; MIHOMO=$2 ;;
                        --warpscout) [ -z "$WARPSCOUT" ] || fail 'Duplicate --warpscout'; WARPSCOUT=$2 ;;
                    esac ;;
            esac
            shift 2 ;;
        --help) printf '%s\n' 'Usage: sh install-warpd.sh [--yes] [--check-only] [--version X.Y.Z] [--language en|ru] [--mihomo /absolute/path] [--warpscout /absolute/path]'; exit 0 ;;
        *) fail "Unknown argument: $1" ;;
    esac
done
# Canonical release versions only: no leading v/zeros, prereleases or shell syntax.
command -v grep >/dev/null 2>&1 || fail 'Missing foundational utility: grep (restore OpenWrt busybox)'
case "$VERSION" in ''|*[!0-9.]*) fail 'Version must be strict X.Y.Z semver' ;; esac
printf '%s\n' "$VERSION" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'Version must be strict X.Y.Z semver'
[ -f /etc/openwrt_release ] || fail 'OpenWrt required'
[ "$(id -u)" = 0 ] || fail 'Root privileges required'
case "$(uname -m)" in aarch64|arm64) ARCH=arm64 ;; x86_64|amd64) ARCH=amd64 ;; *) fail 'Unsupported architecture: arm64/amd64 only' ;; esac
for tool in sh id uname cat cp mv chmod mkdir dirname readlink grep wc ls df awk mktemp rm rmdir ln sleep date; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing foundational utility: $tool; restore OpenWrt base-files/busybox explicitly"
done
now=$(date +%s) || fail 'Cannot read system clock'
case "$now" in ''|*[!0-9]*) fail 'Cannot read system clock' ;; esac
# A clearly stale clock breaks HTTPS; never change time or NTP for the operator.
[ "$now" -ge 1735689600 ] && [ "$now" -le 4102444800 ] || fail 'System clock is not sane for HTTPS; correct date/time manually, then retry (clock/NTP unchanged)'
PACKAGES=
need() { case " $PACKAGES " in *" $1 "*) ;; *) PACKAGES="${PACKAGES:+$PACKAGES }$1" ;; esac; }
requirements() {
    PACKAGES=
    for tool in sha256sum tar gzip uci procd ubus jsonfilter; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            case "$tool" in sha256sum) need coreutils-sha256sum ;; *) need "$tool" ;; esac
        fi
    done
    [ -r /lib/functions/procd.sh ] || need procd
    [ -f /etc/rc.common ] || fail 'Missing OpenWrt /etc/rc.common; restore base-files explicitly'
    command -v curl >/dev/null 2>&1 || need curl
    [ -s /etc/ssl/certs/ca-certificates.crt ] || need ca-bundle
}
requirements
for external in "$MIHOMO" "$WARPSCOUT"; do
    [ -n "$external" ] || continue
    [ -f "$external" ] && [ -x "$external" ] || fail "Dependency is not an executable regular file: $external"
    resolved=$(readlink -f "$external") || fail "Cannot resolve dependency: $external"
    for path in "$external" "$resolved"; do
        case "$path" in
            /usr/libexec/warpd/*|/usr/share/warpd/*|/usr/bin/warpd|/etc/init.d/warpd|/usr/sbin/warpd-uninstall|/etc/config/warpd)
                fail "Dependency points at an installer-owned path: $external" ;;
        esac
        case "$path" in
            /etc/*|*/config|*/config.*|*/config/*|*/configs/*|*.json|*.yaml|*.yml|*.uci|*.conf|*.config|*.key)
                fail "Dependency points at private data or a service configuration: $external" ;;
        esac
    done
    for private in /etc/config/warpd /etc/warpd/warp-account.json /etc/warpd/profiles.json; do
        [ ! "$external" -ef "$private" ] || fail "Dependency aliases private data: $external"
    done
done
PHASE='download preflight'
space() {
    available=$(df -Pk "$1" | awk 'NR > 1 {n=$4} END {print n}')
    case "$available" in ''|*[!0-9]*) fail "Cannot determine free space: $1" ;; esac
    available=$((available * 1024))
    [ "$available" -ge "$2" ] || fail "Insufficient space during $PHASE at $1: need $2 bytes additional, available $available bytes"
}
peak_space() {
    # Add transaction bytes to RAM staging when /tmp shares its backing mount.
    tmp_mount=$(df -Pk /tmp | awk 'NR > 1 {m=$6} END {print m}')
    [ -n "$tmp_mount" ] || fail 'Cannot identify /tmp filesystem for peak space check'
    tmp_need=$1
    for directory in /usr /etc; do
        mount=$(df -Pk "$directory" | awk 'NR > 1 {m=$6} END {print m}')
        [ -n "$mount" ] || fail "Cannot identify filesystem: $directory"
        if [ "$mount" = "$tmp_mount" ]; then
            tmp_need=$((tmp_need + OLD + NEW))
            # /usr and /etc on the same mount share one conservative budget.
            break
        fi
    done
    space /tmp "$tmp_need"
    space /usr "$((OLD + NEW))"
    space /etc "$((OLD + NEW))"
}
# Resolve the longest matching mount, not its device name (tmpfs devices may
# have arbitrary labels). All transaction bytes are conservatively charged once
# if any destination is RAM-backed; this also covers split /usr and /etc.
transaction_ram() {
    RAM_GROWTH=0
    for directory in /usr/bin /usr/libexec/warpd /etc/init.d /usr/sbin /usr/share/warpd /etc/config /etc/warpd; do
        fstype=$(awk -v path="$directory" '
            {m=$2; if ((m=="/" || path==m || index(path,m "/")==1) && length(m)>longest) {longest=length(m); type=$3}}
            END {if (!longest) exit 1; print type}
        ' /proc/mounts) || fail 'Cannot identify destination RAM backing'
        case "$fstype" in tmpfs|ramfs) RAM_GROWTH=$((OLD + NEW)); break ;; esac
    done
}
ram() {
    available=$(awk '/^MemAvailable:/ {a=$2} /^MemFree:/ {f=$2} /^Buffers:/ {b=$2} /^Cached:/ {c=$2} END {if (a) print a; else print f+b+c}' /proc/meminfo)
    case "$available" in ''|*[!0-9]*) fail 'Cannot determine available RAM' ;; esac
    available=$((available * 1024))
    [ "$available" -ge "$1" ] || fail "Insufficient RAM during $PHASE: need $1 bytes additional, available $available bytes (tmpfs storage plus decompressor reserve; not an RSS cap)"
}
OLD=1048576
for target in /usr/bin/warpd /usr/libexec/warpd/mihomo /usr/libexec/warpd/warpscout /etc/init.d/warpd /usr/sbin/warpd-uninstall /etc/config/warpd /etc/warpd/profiles.json; do
    if [ -f "$target" ] && [ ! -L "$target" ]; then
        bytes=$(wc -c < "$target")
        OLD=$((OLD + bytes + 4096))
        [ "$target" != /etc/config/warpd ] || OLD=$((OLD + bytes + 4096))
    fi
done
NEW=12582912
[ -n "$MIHOMO" ] || NEW=$((NEW + 67108864))
[ -n "$WARPSCOUT" ] || NEW=$((NEW + 6291456))
# Select only version-supported, explicitly requested omissions before budgeting.
VARIANT=full
if awk -v v="$VERSION" 'BEGIN {split(v,a,"."); exit !(a[1]>3 || (a[1]==3 && (a[2]>1 || (a[2]==1 && a[3]>=2))))}'; then
    if [ -n "$MIHOMO" ] && [ -n "$WARPSCOUT" ]; then VARIANT=core
    elif [ -n "$MIHOMO" ]; then VARIANT=no-mihomo
    elif [ -n "$WARPSCOUT" ]; then VARIANT=no-warpscout
    fi
fi
case "$VARIANT" in
    full) COMPRESSED_CAP=41943040 ;;
    no-mihomo) COMPRESSED_CAP=12582912 ;;
    no-warpscout) COMPRESSED_CAP=37748736 ;;
    core) COMPRESSED_CAP=8388608 ;;
esac
# The same cap governs initial space, RAM and the bounded fetch. Historical
# releases retain the full ceiling even when their dependencies are reused.
space /tmp "$((COMPRESSED_CAP + 1048576))"
ram "$((COMPRESSED_CAP + 9437184))"
# With wget | ash, stdin contains shell code, not prompt answers. Keep it intact
# and read the controlling terminal through a separate descriptor instead.
INTERACTIVE=0
if [ -t 1 ]; then
    if [ -t 0 ]; then
        exec 3<&0
        INTERACTIVE=1
    elif ( : < /dev/tty ) 2>/dev/null; then
        exec 3< /dev/tty
        INTERACTIVE=1
    fi
fi
if [ -n "$PACKAGES" ]; then
    printf 'Missing required packages: %s\n' "$PACKAGES"
    [ "$CHECK" = 0 ] || fail 'Requirements missing; check-only made no changes'
    if [ "$YES" != 1 ]; then
        [ "$INTERACTIVE" = 1 ] || fail 'Missing requirements; rerun with --yes to permit package installation in noninteractive mode'
        printf '%s' 'Install missing packages / Установить недостающие пакеты? [y/N]: '
        answer=
        IFS= read -r answer <&3 || fail 'No consent (EOF); no packages changed'
        case "$answer" in y|Y|yes|YES|да|Да) ;; *) fail 'Package installation declined / Установка пакетов отменена' ;; esac
    fi
    if command -v apk >/dev/null 2>&1; then PM=apk; ACTION=add
    elif command -v opkg >/dev/null 2>&1; then PM=opkg; ACTION=install
    else fail 'No configured OpenWrt apk/opkg available; restore package manager manually'; fi
    printf '%s\n' 'Installing only missing packages from configured OpenWrt repositories.' 'Package changes are NOT rolled back on failure; common dependencies will not be removed.'
    "$PM" update || fail 'Package index update failed; package manager must bootstrap its own trusted connectivity/CA. No TLS bypass attempted.'
    # PACKAGES contains only the fixed official names produced above.
    "$PM" "$ACTION" $PACKAGES || fail 'Required package installation failed; any installed packages remain'
    hash -r
    requirements
    [ -z "$PACKAGES" ] || fail "Package post-check failed; still missing: $PACKAGES. Packages remain installed."
fi
printf '%s\n' 'Requirements ready.'
[ "$CHECK" = 0 ] || exit 0
if [ -z "$LANGUAGE" ] && [ "$INTERACTIVE" = 1 ]; then
    if [ -e /etc/config/warpd ]; then
        printf '%s' 'UI language / Язык: 1 English, 2 Русский [Enter: keep / сохранить]: '
    else
        printf '%s' 'UI language / Язык: 1 English, 2 Русский [1]: '
    fi
    answer=
    IFS= read -r answer <&3 || answer=
    case "$answer" in
        1|en|English) LANGUAGE=en ;;
        2|ru|Русский) LANGUAGE=ru ;;
        '') [ -e /etc/config/warpd ] || LANGUAGE=en ;;
        *) fail 'Invalid language selection; use --language en|ru' ;;
    esac
fi
# An explicit language edit coexists with the staged config and rollback copy.
if [ -n "$LANGUAGE" ] && [ -f /etc/config/warpd ] && [ ! -L /etc/config/warpd ]; then
    bytes=$(wc -c < /etc/config/warpd)
    OLD=$((OLD + bytes + 4096))
fi
NAME="warpd-$VERSION-linux-$ARCH"
[ "$VARIANT" = full ] || NAME="$NAME-$VARIANT"
URL="https://github.com/LirikKun/warpd-openwrt/releases/download/v$VERSION"
umask 077
WORK=$(mktemp -d /tmp/warpd-bootstrap.XXXXXX) || fail 'Cannot create private temporary directory'
case "$WORK" in /tmp/warpd-bootstrap.*) ;; *) fail 'Unexpected temporary directory path; nothing removed' ;; esac
suffix=${WORK#/tmp/warpd-bootstrap.}
case "$suffix" in ''|*/*) fail 'Unsafe temporary directory path; nothing removed' ;; esac
[ -d "$WORK" ] && [ ! -L "$WORK" ] && [ -z "$(ls -A "$WORK")" ] || fail 'Temporary directory is not fresh; nothing removed'
: > "$WORK/.owned"
cleanup() {
    status=$?
    trap - 0 HUP INT TERM
    if [ -d "$WORK" ] && [ ! -L "$WORK" ] && [ -f "$WORK/.owned" ] && [ ! -L "$WORK/.owned" ]; then
        rm -rf "$WORK" || { printf 'Cannot clean private staging: %s\n' "$WORK" >&2; status=1; }
    else
        printf 'Staging ownership changed; not removed: %s\n' "$WORK" >&2
        status=1
    fi
    if [ "$status" = 0 ] && [ "$SUCCESS" = 1 ]; then
        printf '%s\n' 'Installation complete / Установка завершена.'
    fi
    exit "$status"
}
SUCCESS=0
trap cleanup 0
trap 'exit 1' HUP INT TERM
fetch() {
    attempts=0
    # POSIX ulimit -f counts 512-byte blocks: sidecar remains 64 KiB.
    case "$1" in *.sha256) limit=128 ;; *) limit=$((COMPRESSED_CAP / 512)) ;; esac
    while [ "$attempts" -lt 3 ]; do
        # wget/uclient -T is only an inactivity timeout, not a total deadline.
        if (ulimit -f "$limit" || exit 1; exec curl -fL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 -o "$2" "$1"); then return 0; fi
        attempts=$((attempts + 1))
    done
    fail 'HTTPS download failed; check DNS/connectivity and clock/CA certificates'
}
fetch "$URL/$NAME.tar.gz.sha256" "$WORK/SHA256SUMS"
fetch "$URL/$NAME.tar.gz" "$WORK/$NAME.tar.gz"
COUNT=0
EXPECTED_HASH=
while read -r hash name extra; do
    [ -z "$extra" ] && [ "${#hash}" = 64 ] || fail 'Malformed outer SHA256SUMS'
    case "$hash" in *[!0123456789abcdefABCDEF]*) fail 'Invalid outer checksum' ;; esac
    name=${name#\*}
    case "$name" in ''|*[!A-Za-z0-9._-]*) fail 'Unsafe outer checksum filename' ;; esac
    if [ "$name" = "$NAME.tar.gz" ]; then COUNT=$((COUNT + 1)); EXPECTED_HASH=$hash; fi
done < "$WORK/SHA256SUMS"
[ "$COUNT" = 1 ] || fail 'Outer manifest must name the exact archive once'
printf '%s  %s\n' "$EXPECTED_HASH" "$NAME.tar.gz" > "$WORK/selected.sha256"
(cd "$WORK" && sha256sum -c selected.sha256) || fail 'Checksum verification failed'
# The release builder emits plain USTAR with four directories and 14 files.
# Inspect the verified archive before extraction; only simple regular files/dirs.
(ulimit -f 128 || exit 1; LC_ALL=C tar -tvzf "$WORK/$NAME.tar.gz") > "$WORK/archive.list" || fail 'Cannot list archive'
awk -v root="$NAME" -v variant="$VARIANT" '
    BEGIN {
        split("install.sh uninstall.sh VERSION ARCH SHA256SUMS bin/warpd bin/warpscout bin/mihomo files/warpd.init files/warpd.config README.md THIRD_PARTY.md licenses/warpscout-MIT.txt licenses/mihomo-GPL-3.0.txt", files, " ")
        expected=18
        for (i in files) allowed[root "/" files[i]] = "-"
        if (variant!="full") {allowed[root "/VARIANT"]="-"; expected++}
        if (variant=="core" || variant=="no-mihomo") {delete allowed[root "/bin/mihomo"]; expected--}
        if (variant=="core" || variant=="no-warpscout") {delete allowed[root "/bin/warpscout"]; expected--}
        allowed[root "/"] = "d"; allowed[root "/bin/"] = "d"
        allowed[root "/files/"] = "d"; allowed[root "/licenses/"] = "d"
    }
    {
        path = $6; type = substr($1, 1, 1)
        if (NF != 6 || !(path in allowed) || allowed[path] != type || seen[path]++ || $3 !~ /^[0-9]+$/) bad = 1
        limit=1048576
        if (path ~ /\/bin\//) limit=83886080
        if (path==root "/install.sh" || path==root "/uninstall.sh") limit=131072
        if (path ~ /\/files\//) limit=65536
        if (path==root "/SHA256SUMS") limit=16384
        if (path==root "/ARCH" || path==root "/VERSION" || path==root "/VARIANT") limit=128
        if ($3>limit || (type=="d" && $3!=0)) bad=1
        total += $3
    }
    END { if (bad || NR != expected || total>100663296) exit 1; printf "%.0f\n", total }
' "$WORK/archive.list" > "$WORK/expanded.size" || fail 'Unsafe or unexpected archive members'
EXPANDED=$(cat "$WORK/expanded.size")
# The compressed bytes already occupy free space: budget only further growth.
PHASE='verified control extraction'
space /tmp 1048576
ram 9437184
mkdir "$WORK/$NAME"
CONTROLS='install.sh ARCH VERSION SHA256SUMS'
[ "$VARIANT" = full ] || CONTROLS="$CONTROLS VARIANT"
for member in $CONTROLS; do
    (ulimit -f 256 || exit 1; exec tar -xOzf "$WORK/$NAME.tar.gz" "$NAME/$member") > "$WORK/$NAME/$member" || fail 'Control extraction failed'
    expected_size=$(awk -v p="$NAME/$member" '$6==p {print $3}' "$WORK/archive.list")
    [ "$(wc -c < "$WORK/$NAME/$member")" = "$expected_size" ] || fail 'Control size mismatch'
done
if [ "$VARIANT" != full ]; then
    canonical_line "$WORK/$NAME/VARIANT" "$VARIANT" || fail 'Archive VARIANT metadata does not match requested asset'
fi
ARCHIVE_MODE=0
if grep -qx '# warpd-archive-install-v1' "$WORK/$NAME/install.sh"; then ARCHIVE_MODE=1; fi
NEW=$(awk -v mh="$MIHOMO" -v ws="$WARPSCOUT" -v root="$NAME" '
    $1 ~ /^-/ {
        if ($6 == root "/bin/mihomo" && mh != "") next
        if ($6 == root "/bin/warpscout" && ws != "") next
        n += $3 + 4096
    } END {printf "%.0f\n", n + 262144}
' "$WORK/archive.list")
transaction_ram
if [ "$ARCHIVE_MODE" = 1 ]; then
    PHASE='archive-backed transaction'
    peak_space 1048576
    ram "$((RAM_GROWTH + 9437184))"
else
    PHASE='legacy full extraction and transaction'
    # Old installers need the complete expanded bundle. Compressed and expanded
    # bytes coexist during extraction; df already excludes the downloaded bytes.
    peak_space "$((EXPANDED + 1048576))"
    ram "$((EXPANDED + RAM_GROWTH + 9437184))"
    tar -xzf "$WORK/$NAME.tar.gz" -C "$WORK" || fail 'Archive extraction failed'
    rm -f "$WORK/$NAME.tar.gz"
fi
canonical_line "$WORK/$NAME/ARCH" "$ARCH" || fail 'Malformed ARCH metadata or platform mismatch'
canonical_line "$WORK/$NAME/VERSION" "$VERSION" || fail 'Malformed VERSION metadata or requested release mismatch'
set --
[ "$ARCHIVE_MODE" = 0 ] || set -- --archive "$WORK/$NAME.tar.gz"
[ -z "$LANGUAGE" ] || set -- "$@" --language "$LANGUAGE"
[ -z "$MIHOMO" ] || set -- "$@" --mihomo "$MIHOMO"
[ -z "$WARPSCOUT" ] || set -- "$@" --warpscout "$WARPSCOUT"
TMPDIR="$WORK" sh "$WORK/$NAME/install.sh" "$@" || fail 'Bundle installation failed'
SUCCESS=1
