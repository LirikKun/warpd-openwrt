#!/bin/sh
# Portable OpenWrt installer; no package manager changes or network operations.
# warpd-archive-install-v1
# SHA256SUMS detects corruption, not publisher authenticity.
set -eu
BUNDLE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
fail() { printf 'warpd install: %s\n' "$*" >&2; exit 1; }
# Sanitized scalar values only; compare exact canonical bytes, including LF.
# Both digests read stdin so filenames and shell NUL stripping cannot affect it.
canonical_line() { [ "$(sha256sum < "$1")" = "$(printf '%s\n' "$2" | sha256sum)" ]; }
ROOT=
OFFLINE=0
MIHOMO=
WARPSCOUT=
LANGUAGE=
ARCHIVE=
CONTROL=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --language)
            [ "$#" -ge 2 ] || fail 'Missing value for --language'
            [ -z "$LANGUAGE" ] || fail 'Duplicate --language'
            case "$2" in en|ru) LANGUAGE=$2 ;; *) fail '--language must be en or ru' ;; esac
            shift 2 ;;
        --root|--mihomo|--warpscout|--archive)
            [ "$#" -ge 2 ] || fail "Missing value for $1"
            case "$2" in /*) ;; *) fail "Expected absolute path for $1" ;; esac
            case "$1" in
                --archive) [ -z "$ARCHIVE" ] || fail 'Duplicate --archive'; ARCHIVE=$2 ;;
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
META=$BUNDLE
payload_size() {
    if [ -n "$ARCHIVE" ]; then
        awk -v path="$ARCHIVE_ROOT/$1" '$6 == path {print $3}' "$CONTROL/list"
    else
        wc -c < "$BUNDLE/$1"
    fi
}
# No pipeline may hide the tar producer status. A bounded status file is
# written by the producer even when it emitted valid bytes before failing.
archive_hash() {
    : > "$CONTROL/status"
    (
        result=0
        tar -xOzf "$ARCHIVE" "$ARCHIVE_ROOT/$1" || result=$?
        printf '%s\n' "$result" > "$CONTROL/status"
    ) | sha256sum > "$CONTROL/hash" || fail 'Archive hashing failed'
    [ "$(cat "$CONTROL/status")" = 0 ] || fail "Archive extraction failed: $1"
    read -r actual ignored < "$CONTROL/hash"
    [ "$actual" = "$2" ] || fail "Checksum verification failed: $1"
}
archive_copy() {
    member=$1
    output=$2
    size=$(payload_size "$member")
    case "$size" in ''|*[!0-9]*) fail "Missing archive payload: $member" ;; esac
    # Limit the output file as well as checking its exact final byte count.
    (ulimit -f "$(((size + 511) / 512 + 1))" || exit 1
        exec tar -xOzf "$ARCHIVE" "$ARCHIVE_ROOT/$member") > "$output" || fail "Archive extraction failed: $member"
    [ "$(wc -c < "$output")" = "$size" ] || fail "Archive size mismatch: $member"
}
if [ -n "$ARCHIVE" ]; then
    command -v tar >/dev/null 2>&1 || fail 'Missing prerequisite: tar'
    [ -f "$ARCHIVE" ] && [ ! -L "$ARCHIVE" ] || fail 'Missing regular archive'
    [ "$(wc -c < "$ARCHIVE")" -le 41943040 ] || fail 'Archive exceeds compressed size limit'
    umask 077
    CONTROL=$(mktemp -d "${TMPDIR:-/tmp}/warpd-archive.XXXXXX")
    trap 'rm -rf "$CONTROL"' 0
    trap 'exit 1' HUP INT TERM
    (ulimit -f 128 || exit 1; LC_ALL=C tar -tvzf "$ARCHIVE") > "$CONTROL/list" || fail 'Cannot list archive'
    ARCHIVE_ROOT=$(awk '
        BEGIN {
            split("install.sh uninstall.sh VERSION ARCH VARIANT SHA256SUMS bin/warpd bin/warpscout bin/mihomo files/warpd.init files/warpd.config README.md THIRD_PARTY.md licenses/warpscout-MIT.txt licenses/mihomo-GPL-3.0.txt", f, " ")
            for (i in f) allowed[f[i]] = 1
        }
        {
            path=$6; type=substr($1,1,1)
            if (NR == 1) {root=path; sub(/\/$/, "", root)}
            if (root !~ /^[A-Za-z0-9][A-Za-z0-9._-]*$/ || length(root)>128 || NF!=6 || $3 !~ /^[0-9]+$/ || seen[path]++) bad=1
            if (path==root "/" || path==root "/bin/" || path==root "/files/" || path==root "/licenses/") {
                if (type!="d" || $3!=0) bad=1
                dirs++
            } else {
                name=substr(path,length(root)+2)
                if (substr(path,1,length(root)+1)!=root "/" || !(name in allowed) || type!="-") bad=1
                limit=1048576
                if (name ~ /^bin\//) limit=83886080
                if (name=="install.sh" || name=="uninstall.sh") limit=131072
                if (name ~ /^files\//) limit=65536
                if (name=="SHA256SUMS") limit=16384
                if (name=="ARCH" || name=="VERSION" || name=="VARIANT") limit=128
                if ($3>limit) bad=1
                total+=$3
            }
        }
        END {if (bad || dirs!=4 || NR>19 || NR<16 || total>100663296) exit 1; print root}
    ' "$CONTROL/list") || fail 'Unsafe or unexpected archive members'
    META=$CONTROL
    for member in ARCH VERSION SHA256SUMS; do archive_copy "$member" "$META/$member"; done
    if awk -v p="$ARCHIVE_ROOT/VARIANT" '$6==p {found=1} END {exit !found}' "$CONTROL/list"; then archive_copy VARIANT "$META/VARIANT"; fi
fi
ARCH=$(cat "$META/ARCH")
case "$ARCH" in arm64|amd64) ;; *) fail 'Unsupported ARCH' ;; esac
canonical_line "$META/ARCH" "$ARCH" || fail 'Malformed ARCH metadata'
RELEASE_VERSION=$(cat "$META/VERSION")
printf '%s\n' "$RELEASE_VERSION" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'Malformed VERSION metadata'
case "$RELEASE_VERSION" in *[!0-9.]*) fail 'Malformed VERSION metadata' ;; esac
canonical_line "$META/VERSION" "$RELEASE_VERSION" || fail 'Malformed VERSION metadata'
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
        case "$path" in
            /etc/*|"$ROOT/etc/"*|*/config|*/config.*|*/config/*|*/configs/*|*.json|*.yaml|*.yml|*.uci|*.conf|*.config|*.key)
                fail "Dependency points at private data or a service configuration: $external" ;;
        esac
    done
    # A hardlink alias resolves to a different pathname but the same secret.
    for private in /etc/config/warpd /etc/warpd/warp-account.json /etc/warpd/profiles.json; do
        [ ! "$external" -ef "$ROOT$private" ] || fail "Dependency aliases private data: $external"
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
VARIANT=full
VARIANT_FILE=0
if [ -e "$META/VARIANT" ] || [ -L "$META/VARIANT" ]; then
    [ -f "$META/VARIANT" ] && [ ! -L "$META/VARIANT" ] || fail 'Missing regular VARIANT'
    VARIANT=$(cat "$META/VARIANT")
    case "$VARIANT" in no-mihomo|no-warpscout|core) ;; *) fail 'Malformed VARIANT' ;; esac
    canonical_line "$META/VARIANT" "$VARIANT" || fail 'Malformed VARIANT'
    VARIANT_FILE=1
fi
EXPECTED='install.sh uninstall.sh VERSION ARCH bin/warpd files/warpd.init files/warpd.config README.md THIRD_PARTY.md licenses/warpscout-MIT.txt licenses/mihomo-GPL-3.0.txt'
case "$VARIANT" in
    no-mihomo|core) [ -n "$MIHOMO" ] || fail 'VARIANT requires explicit --mihomo reuse' ;;
    *) EXPECTED="$EXPECTED bin/mihomo" ;;
esac
case "$VARIANT" in
    no-warpscout|core) [ -n "$WARPSCOUT" ] || fail 'VARIANT requires explicit --warpscout reuse' ;;
    *) EXPECTED="$EXPECTED bin/warpscout" ;;
esac
[ "$VARIANT_FILE" = 0 ] || EXPECTED="$EXPECTED VARIANT"
EXPECTED_COUNT=0
for name in $EXPECTED; do EXPECTED_COUNT=$((EXPECTED_COUNT + 1)); done
SEEN=' '
COUNT=0
[ -f "$META/SHA256SUMS" ] && [ ! -L "$META/SHA256SUMS" ] || fail 'Missing regular SHA256SUMS'
while read -r hash name extra; do
    [ -z "$extra" ] && [ "${#hash}" = 64 ] || fail 'Malformed SHA256SUMS'
    case "$hash" in *[!0123456789abcdefABCDEF]*) fail 'Invalid checksum' ;; esac
    hash=$(printf '%s\n' "$hash" | awk '{print tolower($0)}')
    name=${name#\*}
    name=${name#./}
    case " $EXPECTED " in *" $name "*) ;; *) fail "Unexpected checksum path: $name" ;; esac
    case "$SEEN" in *" $name "*) fail "Duplicate checksum: $name" ;; esac
    if [ -n "$ARCHIVE" ]; then
        size=$(payload_size "$name")
        [ -n "$size" ] || fail "Missing regular payload: $name"
        archive_hash "$name" "$hash"
    else
        safe_dir "$(dirname "$BUNDLE/$name")"
        [ -f "$BUNDLE/$name" ] && [ ! -L "$BUNDLE/$name" ] || fail "Missing regular payload: $name"
    fi
    SEEN="$SEEN$name "
    COUNT=$((COUNT + 1))
done < "$META/SHA256SUMS"
[ "$COUNT" = "$EXPECTED_COUNT" ] || fail 'Incomplete SHA256SUMS'
if [ -n "$ARCHIVE" ]; then
    [ "$(wc -l < "$CONTROL/list")" -eq "$((COUNT + 5))" ] || fail 'Archive/checksum coverage mismatch'
else
    (cd "$BUNDLE" && sha256sum -c SHA256SUMS) || fail 'Checksum verification failed'
fi
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
TARGETS='/usr/bin/warpd /usr/libexec/warpd/mihomo /usr/libexec/warpd/warpscout /etc/init.d/warpd /usr/sbin/warpd-uninstall /usr/share/warpd/VERSION /usr/share/warpd/ARCH /usr/share/warpd/OWNER /etc/config/warpd /etc/warpd/profiles.json'
NEEDED=262144
for source in bin/warpd bin/mihomo bin/warpscout files/warpd.init uninstall.sh VERSION ARCH files/warpd.config; do
    case "$source" in
        bin/mihomo) [ -z "$MIHOMO" ] || continue ;;
        bin/warpscout) [ -z "$WARPSCOUT" ] || continue ;;
    esac
    size=$(payload_size "$source")
    NEEDED=$((NEEDED + size + 4096))
done
for target in $TARGETS; do
    dest="$ROOT$target"
    case "$target" in
        /usr/libexec/warpd/mihomo|/usr/libexec/warpd/warpscout) ;;
        *) [ ! -L "$dest" ] || fail "Symlink target refused: $dest" ;;
    esac
    [ ! -e "$dest" ] || [ -f "$dest" ] || fail "Not a regular target: $dest"
    if [ "$target" = /etc/warpd/profiles.json ] && [ -e "$dest" ]; then
        links=$(LC_ALL=C ls -nd "$dest" | awk '{print $2}')
        [ "$links" = 1 ] || fail "Hardlinked profile store refused: $dest"
    fi
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
if [ -n "$LANGUAGE" ]; then
    # The edited output coexists with the staged copy and rollback original.
    config_source="$ROOT/etc/config/warpd"
    if [ -f "$config_source" ]; then size=$(wc -c < "$config_source"); else size=$(payload_size files/warpd.config); fi
    NEEDED=$((NEEDED + size + 4096))
fi
RAM_GROWTH=0
for target in $TARGETS; do
    probe="$ROOT${target%/*}"
    while [ ! -d "$probe" ]; do probe=${probe%/*}; done
    [ -w "$probe" ] || fail "Destination not writable: $probe"
    available=$(df -Pk "$probe" | awk 'NR > 1 {n=$4} END {print n}')
    case "$available" in ''|*[!0-9]*) fail "Cannot determine free space: $probe" ;; esac
    available=$((available * 1024))
    [ "$available" -ge "$NEEDED" ] || fail "Insufficient disk space during transaction at $probe: need $NEEDED bytes additional including rollback backups, available $available bytes"
    if [ "$OFFLINE" = 0 ] && [ "$RAM_GROWTH" = 0 ]; then
        fstype=$(awk -v path="$probe" '
            {m=$2; if ((m=="/" || path==m || index(path,m "/")==1) && length(m)>longest) {longest=length(m); type=$3}}
            END {if (!longest) exit 1; print type}
        ' /proc/mounts) || fail 'Cannot identify destination RAM backing'
        # NEEDED is the entire conservative transaction bound. Charge it once,
        # even if several targets share tmpfs, never charge the retained input.
        case "$fstype" in tmpfs|ramfs) RAM_GROWTH=$NEEDED ;; esac
    fi
done
if [ "$OFFLINE" = 0 ]; then
    available=$(awk '/^MemAvailable:/ {a=$2} /^MemFree:/ {f=$2} /^Buffers:/ {b=$2} /^Cached:/ {c=$2} END {if (a) print a; else print f+b+c}' /proc/meminfo)
    case "$available" in ''|*[!0-9]*) fail 'Cannot determine available RAM' ;; esac
    available=$((available * 1024))
    ram_need=$((RAM_GROWTH + 9437184))
    [ "$available" -ge "$ram_need" ] || fail "Insufficient RAM during transaction: need $ram_need bytes additional, available $available bytes (staging plus process reserve; not an RSS cap)"
fi
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
        rm -f "$stage/new" "$stage/old" "$stage/committed" "$stage/language"
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
    [ -z "$CONTROL" ] || rm -rf "$CONTROL"
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
        case "$source" in
            "$ROOT/etc/config/warpd") cp "$source" "$stage/new" ;;
            "$BUNDLE/"*)
                if [ -n "$ARCHIVE" ]; then
                    member=${source#"$BUNDLE/"}
                    archive_copy "$member" "$stage/new"
                    hash=$(awk -v name="$member" '{p=$2; sub(/^\*/, "", p); sub(/^\.\//, "", p); if(p==name) print tolower($1)}' "$META/SHA256SUMS")
                    read -r actual ignored <<EOF
$(sha256sum "$stage/new")
EOF
                    [ "$actual" = "$hash" ] || fail "Staged checksum verification failed: $member"
                else cp "$source" "$stage/new"; fi ;;
            *) cp "$source" "$stage/new" ;;
        esac
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
if [ -n "$LANGUAGE" ]; then
    # Two passes over the staged file: validate first, then rewrite using only
    # scalar locations. Never retain the file in per-line arrays. Explicit edits
    # refuse records above 64 KiB before tokenization; raw preservation is uncapped.
    config_bytes=$(wc -c < "$stage/new")
    LC_ALL=C awk -v language="$LANGUAGE" -v bytes="$config_bytes" '
        function tokens(line,    i,n,c,q,start,value_start,value) {
            n = 0; i = 1
            while (i <= length(line)) {
                c = substr(line,i,1)
                if (c ~ /[ \t\r]/) { i++; continue }
                if (c == "#") break
                if (n == 3) return -1
                start = i; q = ""
                if (c == "\047" || c == "\042") { q = c; i++ }
                value_start = i
                while (i <= length(line)) {
                    c = substr(line,i,1)
                    if (c == "\\") return -1
                    if (q != "" && c == q) break
                    if (q == "" && c ~ /[ \t\r]/) break
                    if (q == "" && (c == "\047" || c == "\042")) return -1
                    i++
                }
                value = substr(line,value_start,i-value_start)
                if (q != "") {
                    if (i > length(line)) return -1
                    i++
                    if (i <= length(line) && substr(line,i,1) !~ /[ \t\r]/) return -1
                }
                n++; t[n] = value; b[n] = start; e[n] = i - 1; quote[n] = q
            }
            return n
        }
        pass == 1 {
            if (length($0)>65536) { invalid=1; exit 1 }
            n = tokens($0)
            if (n < 0) { invalid = 1; exit 1 }
            if (n == 0) next
            if (t[1] == "config") {
                if (n != 2 && n != 3) { invalid = 1; exit 1 }
                if (t[2] !~ /^[A-Za-z0-9_]+$/ || (n == 3 && t[3] !~ /^[A-Za-z0-9_]+$/)) invalid = 1
                sections++
                global = n == 3 && t[3] == "global"
                if (global) {
                    globals++
                    header = FNR
                    if (t[2] != "warpd") invalid = 1
                }
            } else if ((t[1] == "option" || t[1] == "list") && n == 3) {
                if (!sections || t[2] !~ /^[A-Za-z0-9_]+$/) invalid = 1
                if (global && t[2] == "web_language") {
                    options++
                    if (t[1] != "option") invalid = 1
                    target = FNR; begin_value = b[3]; end_value = e[3]; target_quote = quote[3]
                }
            } else invalid = 1
            next
        }
        pass == 2 {
            if (invalid || globals != 1 || options > 1) { invalid=1; exit 1 }
            consumed += length($0) + 1
            ending = consumed <= bytes ? "\n" : ""
            if (FNR == target)
                printf "%s%s%s%s%s%s", substr($0,1,begin_value-1), target_quote, language, target_quote, substr($0,end_value+1), ending
            else printf "%s%s", $0, ending
            if (options == 0 && FNR == header) {
                if (ending == "") printf "\n"
                printf "    option web_language %c%s%c\n", 39, language, 39
            }
        }
        END {if (invalid || globals != 1 || options > 1) exit 1}
    ' pass=1 "$stage/new" pass=2 "$stage/new" > "$stage/language" || fail 'Cannot set language: unsupported or ambiguous global UCI configuration (explicit edits require 65536-byte line limit)'
    mv -f "$stage/language" "$stage/new"
fi
ensure_dir "$ROOT/etc/warpd"
if [ "$OFFLINE" = 0 ] && [ "$WAS_RUNNING" = 1 ]; then
    SERVICE_CHANGED=1
    stop_wait || fail 'Timed out waiting for warpd and its children to stop; no app files replaced'
fi
# The private directory must be traversable before creating its journal, even
# when an offline root arrived with missing execute bits. finish restores mode.
chmod 700 "$ROOT/etc/warpd"
# Snapshot only after the old daemon stopped: it may persist profiles on exit.
# No new file is staged: absence must remain absence until InitProfiles migrates.
# The ordinary journal restores prior bytes/mode (or absence) if startup fails.
dest="$ROOT/etc/warpd/profiles.json"
[ ! -L "$dest" ] || fail "Symlink target refused: $dest"
[ ! -e "$dest" ] || [ -f "$dest" ] || fail "Not a regular target: $dest"
stage=$(mktemp -d "$ROOT/etc/warpd/.warpd-stage.XXXXXX")
JOURNAL="$stage|$dest
$JOURNAL"
if [ -e "$dest" ]; then cp -Pp "$dest" "$stage/old"; fi
: > "$stage/committed"
while IFS='|' read -r stage dest; do
    [ -n "$stage" ] || continue
    [ -e "$stage/new" ] || [ -L "$stage/new" ] || continue
    # Mark before rename so a signal between the two still restores safely.
    : > "$stage/committed"
    mv -f "$stage/new" "$dest"
done <<EOF
$JOURNAL
EOF
if [ -f "$ROOT/etc/warpd/profiles.json" ]; then chmod 600 "$ROOT/etc/warpd/profiles.json"; fi
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
