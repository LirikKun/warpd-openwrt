"""Offline release-builder tests; run python3 -m unittest discover -s tools -v."""
import gzip
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

import build_release as br


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def test_fetch_validates_fresh_download_and_rechecks_cache(self):
        self.assertTrue(hasattr(br, "fetch_verified"), "verified cache missing")
        data = b"fixture archive"
        digest = hashlib.sha256(data).hexdigest()
        url = "https://example.test/dependency.gz"
        calls = []
        def opener(request, timeout):
            calls.append((request.full_url, timeout))
            return io.BytesIO(data)
        cache = self.root / "cache"
        path = br.fetch_verified(url, digest, cache, opener=opener)
        self.assertEqual(path.read_bytes(), data)
        self.assertEqual(len(calls), 1)
        self.assertEqual(br.fetch_verified(url, digest, cache, opener=opener), path)
        self.assertEqual(len(calls), 1)
        path.write_bytes(b"corrupt cache")
        self.assertEqual(br.fetch_verified(url, digest, cache, opener=opener).read_bytes(), data)
        self.assertEqual(len(calls), 2)
        self.assertFalse(list(cache.glob("*.part")))

    def test_fetch_rejects_checksum_mismatch_without_cache_artifact(self):
        cache = self.root / "cache"
        with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
            br.fetch_verified("https://example.test/bad.gz", "0" * 64, cache,
                              opener=lambda *a, **kw: io.BytesIO(b"untrusted"))
        self.assertEqual(list(cache.iterdir()), [])

    def test_fetch_is_bounded_retries_and_requires_https(self):
        data = b"valid"
        digest = hashlib.sha256(data).hexdigest()
        calls = []
        def opener(request, timeout):
            self.assertGreater(timeout, 0)
            self.assertLessEqual(timeout, 30)
            calls.append(request.full_url)
            if len(calls) < 3:
                raise OSError("temporary network failure")
            return io.BytesIO(data)
        cache = self.root / "cache"
        self.assertTrue(hasattr(br, "MAX_DOWNLOAD_BYTES"), "download bounds missing")
        self.assertEqual(br.fetch_verified("https://example.test/ok.gz", digest, cache,
                                          opener=opener, sleeper=lambda _: None).read_bytes(), data)
        self.assertEqual(len(calls), 3)
        for url in ["http://example.test/a.gz", "file:///etc/passwd"]:
            with self.assertRaisesRegex(ValueError, "HTTPS"):
                br.fetch_verified(url, digest, cache, opener=opener)
        with mock.patch.object(br, "MAX_DOWNLOAD_BYTES", 4):
            with self.assertRaisesRegex(ValueError, "size limit"):
                br.fetch_verified("https://example.test/large.gz", digest, cache,
                                  opener=lambda *a, **kw: io.BytesIO(data))
        fail = mock.Mock(side_effect=OSError("offline"))
        with self.assertRaises(OSError):
            br.fetch_verified("https://example.test/offline.gz", digest, cache,
                              opener=fail, sleeper=lambda _: None)
        self.assertEqual(fail.call_count, 3)
        self.assertFalse(list(cache.glob("*.part")))

    def test_extracts_only_exact_regular_warpscout_member(self):
        self.assertTrue(hasattr(br, "extract_warpscout"), "safe extraction missing")
        archive = self.root / "warpscout.tar.gz"
        def make(entries):
            with tarfile.open(archive, "w:gz") as tar:
                for name, kind, data in entries:
                    info = tarfile.TarInfo(name)
                    info.type = kind
                    info.size = len(data) if kind == tarfile.REGTYPE else 0
                    info.linkname = "warpscout" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ""
                    tar.addfile(info, io.BytesIO(data) if kind == tarfile.REGTYPE else None)
        binary = b"fake executable bytes"
        make([("LICENSE", tarfile.REGTYPE, b"license"), ("warpscout", tarfile.REGTYPE, binary)])
        output = self.root / "bin" / "warpscout"
        br.extract_warpscout(archive, output)
        self.assertEqual(output.read_bytes(), binary)
        self.assertEqual(output.stat().st_mode & 0o777, 0o755)
        self.assertFalse((output.parent / "LICENSE").exists())
        bad_cases = [
            [("../escaped", tarfile.REGTYPE, b"x"), ("warpscout", tarfile.REGTYPE, binary)],
            [("/absolute", tarfile.REGTYPE, b"x"), ("warpscout", tarfile.REGTYPE, binary)],
            [("x/../warpscout", tarfile.REGTYPE, binary)],
            [("bad" + chr(92) + "path", tarfile.REGTYPE, b"x"), ("warpscout", tarfile.REGTYPE, binary)],
            [("warpscout", tarfile.SYMTYPE, b"")],
            [("warpscout", tarfile.LNKTYPE, b"")],
            [("warpscout", tarfile.REGTYPE, binary), ("warpscout", tarfile.REGTYPE, binary)],
            [("sub/warpscout", tarfile.REGTYPE, binary)],
            [("warpscout", tarfile.REGTYPE, b"")],
        ]
        for entries in bad_cases:
            with self.subTest(entries=entries):
                output.unlink(missing_ok=True)
                make(entries)
                with self.assertRaises(ValueError):
                    br.extract_warpscout(archive, output)
                self.assertFalse(output.exists())
        make([("warpscout", tarfile.REGTYPE, binary)])
        with mock.patch.object(br, "MAX_BINARY_BYTES", 4):
            with self.assertRaises(ValueError):
                br.extract_warpscout(archive, output)
        with mock.patch.object(br, "MAX_ARCHIVE_BYTES", 8):
            with self.assertRaises(ValueError):
                br.extract_warpscout(archive, output)
        self.assertFalse((self.root / "escaped").exists())

    def test_mihomo_gzip_is_bounded_and_never_executed(self):
        self.assertTrue(hasattr(br, "extract_mihomo"), "mihomo decompression missing")
        archive = self.root / "mihomo.gz"
        archive.write_bytes(gzip.compress(b"binary fixture"))
        output = self.root / "bin" / "mihomo"
        br.extract_mihomo(archive, output)
        self.assertEqual(output.read_bytes(), b"binary fixture")
        self.assertEqual(output.stat().st_mode & 0o777, 0o755)
        output.unlink()
        with mock.patch.object(br, "MAX_BINARY_BYTES", 4):
            with self.assertRaises(ValueError):
                br.extract_mihomo(archive, output)
        self.assertFalse(output.exists())
        archive.write_bytes(gzip.compress(b""))
        with self.assertRaises(ValueError):
            br.extract_mihomo(archive, output)

    def test_crosscompile_uses_explicit_go_and_reproducible_linux_environment(self):
        self.assertTrue(hasattr(br, "compile_warpd"), "crosscompile missing")
        go = self.root / "fake-go"
        go.write_text('#!/usr/bin/env python3\nimport json, os, pathlib, sys\npathlib.Path(os.environ["CAPTURE"]).write_text(json.dumps({"argv":sys.argv[1:], "env":dict(os.environ), "cwd":os.getcwd()}))\npathlib.Path(sys.argv[sys.argv.index("-o")+1]).write_bytes(b"fixture warpd")\n')
        go.chmod(0o755)
        capture = self.root / "capture.json"
        import json
        output = self.root / "bin" / "warpd"
        for arch in ("arm64", "amd64"):
            with mock.patch.dict(os.environ, {"GO": str(go), "CAPTURE": str(capture), "GOFLAGS": "-race", "CGO_ENABLED": "1", "GOOS": "windows"}):
                br.compile_warpd(self.root, arch, output)
            result = json.loads(capture.read_text())
            self.assertEqual(result["argv"], ["build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid=", "-o", str(output), "."])
            for name, value in {"GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOFLAGS": "", "GOAMD64": "v1", "GOARM64": "v8.0", "GOWORK": "off", "GOENV": "off", "GOEXPERIMENT": ""}.items():
                self.assertEqual(result["env"][name], value)
            self.assertEqual(result["cwd"], str(self.root))
            self.assertEqual(output.read_bytes(), b"fixture warpd")
        with mock.patch.dict(os.environ, {"GO": str(self.root / "missing-go")}):
            with self.assertRaises(FileNotFoundError):
                br.compile_warpd(self.root, "arm64", output)

    def test_epoch_uses_source_date_epoch_or_commit_timestamp_with_bounds(self):
        self.assertTrue(hasattr(br, "release_epoch"), "reproducible timestamp missing")
        with mock.patch.dict(os.environ, {"SOURCE_DATE_EPOCH": "1234567890"}):
            self.assertEqual(br.release_epoch(self.root), 1234567890)
        for value in ("-1", "4294967296", "1.5", "", "unknown"):
            with mock.patch.dict(os.environ, {"SOURCE_DATE_EPOCH": value}):
                with self.assertRaises(ValueError):
                    br.release_epoch(self.root)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        env = dict(os.environ, GIT_AUTHOR_DATE="2001-01-01T00:00:00Z", GIT_COMMITTER_DATE="2001-01-01T00:00:00Z")
        subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-q", "--allow-empty", "-m", "fixture"], cwd=self.root, env=env, check=True)
        with mock.patch.dict(os.environ):
            os.environ.pop("SOURCE_DATE_EPOCH", None)
            self.assertEqual(br.release_epoch(self.root), 978307200)

    def fixture_sources(self):
        sources = {
            "install.sh": "packaging/install.sh", "uninstall.sh": "packaging/uninstall.sh",
            "files/warpd.init": "warpd.init", "files/warpd.config": "packaging/warpd.config",
            "README.md": "README.md", "THIRD_PARTY.md": "THIRD_PARTY.md",
            "licenses/warpscout-MIT.txt": "packaging/licenses/warpscout-MIT.txt",
            "licenses/mihomo-GPL-3.0.txt": "packaging/licenses/mihomo-GPL-3.0.txt",
        }
        for dest, source in sources.items():
            path = self.root / source
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(("fixture " + source + "\n").encode())
        (self.root / "warp-account.json").write_text("secret must never ship")
        (self.root / "local.key").write_text("another secret")
        bins = {}
        for name in ("warpd", "warpscout", "mihomo"):
            path = self.root / "binaries" / name
            path.parent.mkdir(exist_ok=True)
            path.write_bytes(("binary " + name).encode())
            bins[name] = path
        return sources, bins

    def test_archive_is_fixed_allowlist_deterministic_and_checksummed(self):
        self.assertTrue(hasattr(br, "create_archive"), "portable archive missing")
        sources, binaries = self.fixture_sources()
        epoch = 1234567890
        archive = br.create_archive(self.root, binaries, self.root / "out", "2.1.1", "arm64", epoch)
        expected_root = "warpd-2.1.1-linux-arm64"
        self.assertEqual(archive.name, expected_root + ".tar.gz")
        first = archive.read_bytes()
        for source in sources.values():
            os.utime(self.root / source, (100, 100))
            (self.root / source).chmod(0o777)
        self.assertEqual(br.create_archive(self.root, binaries, self.root / "out", "2.1.1", "arm64", epoch).read_bytes(), first)
        self.assertEqual(int.from_bytes(first[4:8], "little"), epoch)
        expected_files = set(sources) | {"bin/warpd", "bin/warpscout", "bin/mihomo", "ARCH", "VERSION", "SHA256SUMS"}
        with tarfile.open(archive, "r:gz") as tar:
            members = tar.getmembers()
            self.assertEqual([m.name for m in members], sorted(m.name for m in members))
            regular = {m.name.removeprefix(expected_root + "/"): m for m in members if m.isfile()}
            self.assertEqual(set(regular), expected_files)
            for member in members:
                self.assertEqual((member.uid, member.gid, member.uname, member.gname, member.mtime), (0, 0, "", "", epoch))
                self.assertFalse(member.issym() or member.islnk())
            contents = {name: tar.extractfile(member).read() for name, member in regular.items()}
            self.assertEqual(contents["VERSION"], b"2.1.1\n")
            self.assertEqual(contents["ARCH"], b"arm64\n")
            sum_names = []
            for line in contents["SHA256SUMS"].decode().splitlines():
                digest, name = line.split("  ", 1)
                self.assertEqual(digest, hashlib.sha256(contents[name]).hexdigest())
                sum_names.append(name)
            self.assertEqual(sum_names, sorted(expected_files - {"SHA256SUMS"}))
            for name, member in regular.items():
                expected_mode = 0o755 if name.startswith("bin/") or name in ("install.sh", "uninstall.sh", "files/warpd.init") else (0o600 if name == "files/warpd.config" else 0o644)
                self.assertEqual(member.mode, expected_mode, name)
        self.assertEqual(Path(str(archive) + ".sha256").read_text(), hashlib.sha256(first).hexdigest() + "  " + archive.name + "\n")
        self.assertEqual(len(expected_files), 14)

    def test_main_builds_complete_release_offline_with_cache_and_output_options(self):
        self.assertTrue(hasattr(br, "main"), "release command missing")
        self.fixture_sources()
        (self.root / "main.go").write_text('const version = "2.1.1"\n')
        go = self.root / "go"
        go.write_text('#!/usr/bin/env python3\nimport pathlib,sys\npathlib.Path(sys.argv[sys.argv.index("-o")+1]).write_bytes(b"compiled fixture")\n')
        go.chmod(0o755)
        ws = io.BytesIO()
        with tarfile.open(fileobj=ws, mode="w:gz") as tar:
            info = tarfile.TarInfo("warpscout")
            info.size = 7
            tar.addfile(info, io.BytesIO(b"scanner"))
        deps = br.dependencies("arm64")
        fixtures = {deps["warpscout"]["url"]: ws.getvalue(), deps["mihomo"]["url"]: gzip.compress(b"engine")}
        for pin in deps.values():
            pin["sha256"] = hashlib.sha256(fixtures[pin["url"]]).hexdigest()
        requests = []
        def opener(request, timeout):
            requests.append(request.full_url)
            return io.BytesIO(fixtures[request.full_url])
        out, cache = self.root / "chosen output", self.root / "chosen cache"
        args = ["--cache-dir", str(cache), "--output-dir", str(out)]
        with mock.patch.dict(os.environ, {"GO": str(go), "SOURCE_DATE_EPOCH": "1234567890"}), mock.patch.object(br, "dependencies", return_value=deps):
            with mock.patch("sys.stdout", new_callable=io.StringIO):
                self.assertEqual(br.main(args, repo=self.root, opener=opener), 0)
                first = (out / "warpd-2.1.1-linux-arm64.tar.gz").read_bytes()
                self.assertEqual(br.main(args, repo=self.root, opener=opener), 0)
            self.assertEqual(len(requests), 2)
        self.assertEqual(len(list(out.iterdir())), 2)
        self.assertEqual((out / "warpd-2.1.1-linux-arm64.tar.gz").read_bytes(), first)
        with self.assertRaises(SystemExit) as error, mock.patch("sys.stderr", new_callable=io.StringIO):
            br.main(["--arch", "mips"], repo=self.root, opener=opener)
        self.assertEqual(error.exception.code, 2)

    def test_build_sh_is_executable_portable_entrypoint_with_help(self):
        script = Path(__file__).resolve().parent.parent / "build.sh"
        self.assertTrue(script.read_text().startswith("#!/bin/sh\n"), "build.sh needs a runnable shell entrypoint")
        self.assertTrue(os.access(script, os.X_OK))
        result = subprocess.run([str(script), "--help"], cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        for option in ("--arch", "--cache-dir", "--output-dir", "GO=", "SOURCE_DATE_EPOCH"):
            self.assertIn(option, result.stdout)
        bad = subprocess.run([str(script), "--arch", "mips"], cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(bad.returncode, 2)

    def test_https_redirect_downgrade_and_download_deadline_are_rejected(self):
        self.assertTrue(hasattr(br, "HTTPSOnlyRedirectHandler"), "HTTPS redirect policy missing")
        handler = br.HTTPSOnlyRedirectHandler()
        request = br.urllib.request.Request("https://example.test/dep.gz")
        with self.assertRaisesRegex(ValueError, "HTTPS"):
            handler.redirect_request(request, None, 302, "Found", {}, "http://example.test/dep.gz")
        redirected = handler.redirect_request(request, None, 302, "Found", {}, "https://cdn.example.test/dep.gz")
        self.assertEqual(redirected.full_url, "https://cdn.example.test/dep.gz")
        ticks = iter(range(0, 100000, 121))
        opener = mock.Mock(side_effect=lambda *a, **kw: io.BytesIO(b"data"))
        with mock.patch.object(br.time, "monotonic", side_effect=lambda: next(ticks)):
            with self.assertRaises(TimeoutError):
                br.fetch_verified("https://example.test/timeout.gz", "0" * 64,
                                  self.root / "cache", opener=opener, sleeper=lambda _: None)
        self.assertEqual(opener.call_count, 3)
        self.assertEqual(list((self.root / "cache").iterdir()), [])

    def test_archive_rejects_symlinked_release_sources(self):
        _, binaries = self.fixture_sources()
        source = self.root / "README.md"
        source.unlink()
        source.symlink_to(self.root / "warp-account.json")
        with self.assertRaisesRegex(ValueError, "regular file"):
            br.create_archive(self.root, binaries, self.root / "out", "2.1.1", "arm64", 123)
        self.assertFalse((self.root / "out" / "warpd-2.1.1-linux-arm64.tar.gz").exists())

    def test_download_cannot_complete_after_its_deadline(self):
        now = [0]
        class SlowEOF(io.BytesIO):
            def read(self, size=-1):
                chunk = super().read(size)
                if not chunk:
                    now[0] += 121
                return chunk
            read1 = read
        with mock.patch.object(br.time, "monotonic", side_effect=lambda: now[0]):
            with self.assertRaises(TimeoutError):
                br.fetch_verified("https://example.test/slow-eof.gz", hashlib.sha256(b"data").hexdigest(),
                                  self.root / "cache", opener=lambda *a, **kw: SlowEOF(b"data"), sleeper=lambda _: None)
        self.assertEqual(list((self.root / "cache").iterdir()), [])

    def test_download_reads_single_chunks_for_socket_timeout_bounds(self):
        class ChunkedResponse(io.BytesIO):
            def read(self, size=-1):
                raise AssertionError("read may refill repeatedly; use one bounded read1")
            def read1(self, size=-1):
                return super().read(size)
        data = b"chunk fixture"
        path = br.fetch_verified("https://example.test/chunked.gz", hashlib.sha256(data).hexdigest(),
                                 self.root / "cache", opener=lambda *a, **kw: ChunkedResponse(data))
        self.assertEqual(path.read_bytes(), data)

    def test_architecture_selects_exact_pins_and_rejects_unsupported(self):
        self.assertTrue(hasattr(br, "dependencies"), "architecture pins missing")
        for arch, ws_hash, mh_hash, mh_name in [
            ("arm64", "261ef88a1ac6eb405ea2d11d80fcce823913ae1faaff9cdc1dff4465c516483d", "9e0f11afbf38426b8bd88fdc594678f8161c57eccb4e1b77acb12b493904f1d4", "mihomo-linux-arm64-v1.19.31.gz"),
            ("amd64", "67a10e3e17b83912a44c72f6e486490c0cbb90d19a51111cec32d5507d6a0f5c", "d4304c546c3cddcb6fafd4b4fddb0ba1a95ffa36606fda56d75db2e59ad24114", "mihomo-linux-amd64-v1-v1.19.31.gz"),
        ]:
            deps = br.dependencies(arch)
            self.assertEqual(deps["warpscout"]["sha256"], ws_hash)
            self.assertEqual(deps["mihomo"]["sha256"], mh_hash)
            self.assertEqual(deps["warpscout"]["url"], "https://github.com/vernette/warpscout/releases/download/v0.16.0/warpscout_0.16.0_linux_" + arch + ".tar.gz")
            self.assertEqual(deps["mihomo"]["url"], "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.31/" + mh_name)
        with self.assertRaises(ValueError):
            br.dependencies("mips")

    def test_dynamic_profile_v3_bundle_uses_private_seed_and_excludes_runtime_secrets(self):
        import shutil
        repo = Path(__file__).resolve().parent.parent
        for source in br.BUNDLE_SOURCES.values():
            destination = self.root / source
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(repo / source, destination)
        (self.root / 'main.go').write_text('const version = "3.0.0"\n')
        secret = self.root / 'etc/warpd/profiles.json'
        secret.parent.mkdir(parents=True)
        secret.write_bytes(b'TEST PRIVATE RELAY CAPABILITY MUST NEVER SHIP')
        (secret.parent / 'profiles.json.user-backup').write_bytes(b'TEST UNKNOWN BACKUP')
        binaries = {}
        for name in ('warpd', 'mihomo', 'warpscout'):
            binary = self.root / ('test-' + name)
            binary.write_text('#!/bin/sh\n# TEST executable fixture, not a distributable binary\nexit 0\n')
            binaries[name] = binary
        version = br.read_version(self.root / 'main.go')
        archive = br.create_archive(self.root, binaries, self.root / 'out', version, 'arm64', 123)
        self.assertEqual(archive.name, 'warpd-3.0.0-linux-arm64.tar.gz')
        extracted = self.root / 'extracted'
        with tarfile.open(archive, 'r:gz') as tar:
            contents = {m.name: tar.extractfile(m).read() for m in tar if m.isfile()}
            self.assertFalse(any('profiles.json' in name for name in contents))
            self.assertFalse(any(b'TEST PRIVATE RELAY' in value for value in contents.values()))
            tar.extractall(extracted, filter='data')
        bundle = extracted / 'warpd-3.0.0-linux-arm64'
        target = self.root / 'offline-root'
        target.mkdir()
        def run(script, *args):
            result = subprocess.run(['sh', str(bundle / script), '--root', str(target), *args],
                                    text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        run('install.sh')
        config = (target / 'etc/config/warpd').read_bytes()
        self.assertIn(b"option profile_schema '1'", config)
        self.assertEqual(config.count(b'config warp '), 1)
        profile = target / 'etc/warpd/profiles.json'
        self.assertFalse(profile.exists(), 'bundle must leave migration to InitProfiles')
        original = b'{"schema":1,"profiles":{},"relays":{}}\n'
        profile.write_bytes(original)
        profile.chmod(0o600)
        run('install.sh')
        self.assertEqual(profile.read_bytes(), original)
        self.assertEqual((target / 'etc/config/warpd').read_bytes(), config)
        self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
        run('uninstall.sh')
        self.assertEqual(profile.read_bytes(), original)
        run('uninstall.sh', '--purge')
        self.assertFalse(profile.exists())
        self.assertEqual(secret.read_bytes(), b'TEST PRIVATE RELAY CAPABILITY MUST NEVER SHIP')

    def test_reads_only_exact_version_constant(self):
        self.assertIsNotNone(br, "release builder is not implemented")
        source = self.root / "main.go"
        source.write_text('const other = "9.9.9"\nconst version = "2.1.1"\n')
        self.assertEqual(br.read_version(source), "2.1.1")
        for text in ['// const version = "2.1.1"', 'var version = "2.1.1"',
                     'const version = "../../escape"',
                     'const version = "2.1.1"\nconst version = "2.1.2"']:
            source.write_text(text)
            with self.assertRaises(ValueError):
                br.read_version(source)


if __name__ == "__main__":
    unittest.main()
