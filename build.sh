#!/bin/sh
# Host-only portable release builder; run ./build.sh --help for options.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$ROOT/tools/build_release.py" "$@"
