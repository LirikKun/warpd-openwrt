# Build and package warpd for OpenWrt 25.12 (mediatek/filogic, aarch64).
#
# Produces:
#   dist/warpd_1.0.0_aarch64_cortex-a53.apk  — warpd + init + uci-defaults
#   dist/warpscout_0.16.0_aarch64_cortex-a53.apk — upstream binary, repackaged
#
# Both are built on the build host (cross-compile) and packed with apk mkpkg
# ON THE ROUTER (the host has no apk-tools). run.sh handles that.
set -e
cd "$(dirname "$0")"
export PATH="$HOME/go-toolchain/go/bin:$PATH"
export GOTOOLCHAIN=local

mkdir -p dist

echo "== building warpd (aarch64) =="
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o dist/warpd .
ls -la dist/warpd

echo "== fetching warpscout 0.16.0 arm64 =="
WS=dist/warpscout
if [ ! -x "$WS" ]; then
    curl -fsSL -o dist/warpscout.tar.gz https://github.com/vernette/warpscout/releases/download/v0.16.0/warpscout_0.16.0_linux_arm64.tar.gz
    tar xzf dist/warpscout.tar.gz -C dist warpscout
    rm dist/warpscout.tar.gz
fi
ls -la "$WS"

echo "done: binaries in dist/"
