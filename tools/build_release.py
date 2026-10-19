#!/usr/bin/env python3
"""Build a pinned, reproducible portable warpd release using stdlib + Go."""
import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request


BUNDLE_SOURCES = {
    "install.sh": "packaging/install.sh",
    "uninstall.sh": "packaging/uninstall.sh",
    "files/warpd.init": "warpd.init",
    "files/warpd.config": "packaging/warpd.config",
    "README.md": "README.md",
    "THIRD_PARTY.md": "THIRD_PARTY.md",
    "licenses/warpscout-MIT.txt": "packaging/licenses/warpscout-MIT.txt",
    "licenses/mihomo-GPL-3.0.txt": "packaging/licenses/mihomo-GPL-3.0.txt",
}


VARIANT_BINARIES = {
    "full": ("warpd", "warpscout", "mihomo"),
    "no-mihomo": ("warpd", "warpscout"),
    "no-warpscout": ("warpd", "mihomo"),
    "core": ("warpd",),
}


def create_archive(repo, binaries, output_dir, version, arch, epoch, variant="full"):
    dependencies(arch)
    if not isinstance(variant, str) or variant not in VARIANT_BINARIES:
        raise ValueError("supported variants: " + ", ".join(VARIANT_BINARIES))
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise ValueError("invalid release version")
    if not isinstance(epoch, int) or not 0 <= epoch <= 0xFFFFFFFF:
        raise ValueError("release timestamp out of bounds")
    paths = {name: Path(repo) / source for name, source in BUNDLE_SOURCES.items()}
    for name in VARIANT_BINARIES[variant]:
        if name not in binaries:
            raise ValueError("required release binary missing: " + name)
        paths["bin/" + name] = Path(binaries[name])
    for path in paths.values():
        if path.is_symlink() or not path.is_file():
            raise ValueError("release source must be a regular file, not a symlink: " + str(path))
    files = {name: path.read_bytes() for name, path in paths.items()}
    files.update(VERSION=(version + "\n").encode(), ARCH=(arch + "\n").encode())
    if variant != "full":
        files["VARIANT"] = (variant + "\n").encode()
    files["SHA256SUMS"] = "".join(
        hashlib.sha256(files[name]).hexdigest() + "  " + name + "\n"
        for name in sorted(files)).encode()
    root = f"warpd-{version}-linux-{arch}"
    if variant != "full":
        root += "-" + variant
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    archive = output_dir / (root + ".tar.gz")
    partial = None
    try:
        with tempfile.NamedTemporaryFile(dir=output_dir, suffix=".part", delete=False) as raw:
            partial = Path(raw.name)
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch, compresslevel=9) as gz:
                with tarfile.open(fileobj=gz, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
                    entries = {root: None, **{root + "/" + name: None for name in ("bin", "files", "licenses")},
                               **{root + "/" + name: data for name, data in files.items()}}
                    for name in sorted(entries):
                        data = entries[name]
                        info = tarfile.TarInfo(name)
                        info.mtime = epoch
                        relative = name[len(root) + 1:]
                        if data is None:
                            info.type = tarfile.DIRTYPE
                            info.mode = 0o755
                        else:
                            info.size = len(data)
                            info.mode = (0o755 if relative.startswith("bin/") or relative in
                                         ("install.sh", "uninstall.sh", "files/warpd.init") else
                                         0o600 if relative == "files/warpd.config" else 0o644)
                        tar.addfile(info, io.BytesIO(data) if data is not None else None)
        partial.chmod(0o644)
        os.replace(partial, archive)
    finally:
        if partial is not None:
            partial.unlink(missing_ok=True)
    Path(str(archive) + ".sha256").write_text(
        hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
    return archive


def release_epoch(repo):
    value = os.environ.get("SOURCE_DATE_EPOCH")
    if value is None:
        value = subprocess.check_output(["git", "log", "-1", "--format=%ct", "HEAD"],
                                        cwd=repo, text=True).strip()
    if not re.fullmatch(r"[0-9]+", value) or not 0 <= int(value) <= 0xFFFFFFFF:
        raise ValueError("SOURCE_DATE_EPOCH/commit timestamp must be 0..4294967295")
    return int(value)


def compile_warpd(repo, arch, output):
    dependencies(arch)  # Validate before invoking any tool.
    go = os.environ.get("GO") or shutil.which("go")
    if not go:
        go = str(Path.home() / "go-toolchain/go/bin/go")
    if not shutil.which(go):
        raise FileNotFoundError("Go compiler not found; set GO=/path/to/go")
    output = Path(output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0",
               GOTOOLCHAIN="local", GOFLAGS="", GOAMD64="v1", GOARM64="v8.0",
               GOWORK="off", GOENV="off", GOEXPERIMENT="")
    subprocess.run([go, "build", "-trimpath", "-buildvcs=false", "-ldflags",
                    "-s -w -buildid=", "-o", str(output), "."],
                   cwd=repo, env=env, check=True)
    output.chmod(0o755)


MAX_BINARY_BYTES = 128 * 1024 * 1024
MAX_ARCHIVE_BYTES = 160 * 1024 * 1024


def extract_mihomo(archive, output):
    with gzip.open(archive, "rb") as stream:
        binary = stream.read(MAX_BINARY_BYTES + 1)
    if not binary or len(binary) > MAX_BINARY_BYTES:
        raise ValueError("mihomo binary empty or exceeds size limit")
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(binary)
    output.chmod(0o755)


def extract_warpscout(archive, output):
    # Never extract paths from upstream archives. Validate the complete bounded
    # stream, then copy only the one exact regular binary into our staging tree.
    with gzip.open(archive, "rb") as stream:
        expanded = stream.read(MAX_ARCHIVE_BYTES + 1)
    if len(expanded) > MAX_ARCHIVE_BYTES:
        raise ValueError("warpscout archive exceeds expanded size limit")
    binary = None
    seen = set()
    with tarfile.open(fileobj=io.BytesIO(expanded), mode="r:") as tar:
        for member in tar:
            path = PurePosixPath(member.name)
            if (len(seen) >= 128 or member.name in seen or path.is_absolute()
                    or ".." in path.parts or "\\" in member.name
                    or not (member.isfile() or member.isdir())
                    or member.size < 0 or member.size > MAX_BINARY_BYTES):
                raise ValueError("unsafe or oversized warpscout archive member")
            seen.add(member.name)
            if member.name == "warpscout":
                if not member.isfile() or member.size == 0:
                    raise ValueError("warpscout must be a nonempty regular file")
                binary = tar.extractfile(member).read(MAX_BINARY_BYTES + 1)
                if len(binary) != member.size:
                    raise ValueError("truncated warpscout binary")
    if binary is None:
        raise ValueError("archive lacks exact warpscout binary")
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(binary)
    output.chmod(0o755)

MAX_DOWNLOAD_BYTES = 64 * 1024 * 1024
DOWNLOAD_DEADLINE = 120


class HTTPSOnlyRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if not newurl.startswith("https://"):
            raise ValueError("dependency redirect requires HTTPS")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def fetch_verified(url, expected, cache_dir, opener=None, sleeper=time.sleep):
    if not url.startswith("https://"):
        raise ValueError("dependency downloads require HTTPS")
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("invalid pinned SHA-256")
    cache = Path(cache_dir)
    cache.mkdir(parents=True, exist_ok=True)
    name = url.rsplit("/", 1)[-1]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", name):
        raise ValueError("invalid dependency filename")
    target = cache / name
    if target.is_file() and not target.is_symlink() and target.stat().st_size <= MAX_DOWNLOAD_BYTES:
        if hashlib.sha256(target.read_bytes()).hexdigest() == expected:
            return target
    target.unlink(missing_ok=True)
    request = urllib.request.Request(url, headers={"User-Agent": "warpd-release-builder"})
    for attempt in range(3):
        partial = None
        try:
            deadline = time.monotonic() + DOWNLOAD_DEADLINE
            open_url = opener or urllib.request.build_opener(HTTPSOnlyRedirectHandler()).open
            with open_url(request, timeout=30) as response:
                with tempfile.NamedTemporaryFile(dir=cache, suffix=".part", delete=False) as output:
                    partial = Path(output.name)
                    digest = hashlib.sha256()
                    size = 0
                    while True:
                        if time.monotonic() > deadline:
                            raise TimeoutError("dependency download deadline exceeded")
                        # HTTPResponse.read1 performs at most one socket read;
                        # read(n) may keep refilling under a slow-drip response.
                        chunk = response.read1(64 * 1024)
                        if time.monotonic() > deadline:
                            raise TimeoutError("dependency download deadline exceeded")
                        if not chunk:
                            break
                        size += len(chunk)
                        if size > MAX_DOWNLOAD_BYTES:
                            raise ValueError("dependency download exceeds size limit")
                        digest.update(chunk)
                        output.write(chunk)
            if digest.hexdigest() != expected:
                raise ValueError("SHA-256 mismatch: " + name)
            os.replace(partial, target)
            return target
        except OSError:
            if attempt == 2:
                raise
            sleeper(attempt + 1)
        finally:
            if partial is not None:
                partial.unlink(missing_ok=True)
    raise AssertionError("unreachable")


def dependencies(arch):
    if arch not in ("arm64", "amd64"):
        raise ValueError("supported architectures: arm64, amd64")
    ws_hash, mh_hash, mh_arch = {
        "arm64": ("261ef88a1ac6eb405ea2d11d80fcce823913ae1faaff9cdc1dff4465c516483d",
                  "9e0f11afbf38426b8bd88fdc594678f8161c57eccb4e1b77acb12b493904f1d4", "arm64"),
        "amd64": ("67a10e3e17b83912a44c72f6e486490c0cbb90d19a51111cec32d5507d6a0f5c",
                  "d4304c546c3cddcb6fafd4b4fddb0ba1a95ffa36606fda56d75db2e59ad24114", "amd64-v1"),
    }[arch]
    return {
        "warpscout": {"url": f"https://github.com/vernette/warpscout/releases/download/v0.16.0/warpscout_0.16.0_linux_{arch}.tar.gz", "sha256": ws_hash},
        "mihomo": {"url": f"https://github.com/MetaCubeX/mihomo/releases/download/v1.19.31/mihomo-linux-{mh_arch}-v1.19.31.gz", "sha256": mh_hash},
    }


def read_version(source):
    matches = re.findall(r'^const version = "([0-9]+\.[0-9]+\.[0-9]+)"\s*$',
                         Path(source).read_text(), re.MULTILINE)
    if len(matches) != 1:
        raise ValueError("main.go must contain exactly one const version = X.Y.Z")
    return matches[0]


def main(argv=None, *, repo=None, opener=None):
    repo = Path(repo) if repo is not None else Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(
        description="Build a standalone portable warpd bundle (host Python 3 + Go; no router/root).",
        epilog="Set GO=/path/to/go to choose Go; otherwise PATH then ~/go-toolchain/go/bin/go. "
               "SOURCE_DATE_EPOCH overrides the current commit timestamp (0..4294967295). "
               "Dependencies are downloaded over HTTPS and checked against pinned SHA-256 hashes.")
    parser.add_argument("--arch", choices=("arm64", "amd64"), default="arm64", help="Linux target (default: arm64)")
    parser.add_argument("--variant", choices=tuple(VARIANT_BINARIES), default="full",
                        help="bundled binaries (default: full; core includes only warpd)")
    parser.add_argument("--cache-dir", type=Path, default=repo / "dist/cache", help="verified download cache (default: dist/cache)")
    parser.add_argument("--output-dir", type=Path, default=repo / "dist", help="archive output directory (default: dist)")
    args = parser.parse_args(argv)
    try:
        version = read_version(repo / "main.go")
        epoch = release_epoch(repo)
        for source in BUNDLE_SOURCES.values():
            if not (repo / source).is_file():
                raise FileNotFoundError("required release source missing: " + source)
        pins = dependencies(args.arch)
        cached = {name: fetch_verified(pin["url"], pin["sha256"], args.cache_dir, opener=opener)
                  for name, pin in pins.items() if name in VARIANT_BINARIES[args.variant]}
        with tempfile.TemporaryDirectory(prefix="warpd-release-") as staging:
            binaries = {name: Path(staging) / name for name in VARIANT_BINARIES[args.variant]}
            if "warpscout" in cached:
                extract_warpscout(cached["warpscout"], binaries["warpscout"])
            if "mihomo" in cached:
                extract_mihomo(cached["mihomo"], binaries["mihomo"])
            compile_warpd(repo, args.arch, binaries["warpd"])
            archive = create_archive(repo, binaries, args.output_dir, version, args.arch, epoch,
                                     variant=args.variant)
        print(archive)
        print(str(archive) + ".sha256")
        return 0
    except (OSError, ValueError, tarfile.TarError, EOFError, subprocess.CalledProcessError) as error:
        print("build failed: " + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
