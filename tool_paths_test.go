package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolBinaryPrefersPrivateInstallation(t *testing.T) {
	dir := t.TempDir()
	preferred := filepath.Join(dir, "private-tool")
	legacy := filepath.Join(dir, "legacy-tool")
	if err := os.WriteFile(preferred, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := toolBinary(preferred, legacy); got != preferred {
		t.Fatalf("selected %q, want private %q", got, preferred)
	}
}

func TestToolBinaryFallsBackOnlyWhenPrivatePathMissing(t *testing.T) {
	dir := t.TempDir()
	preferred := filepath.Join(dir, "absent")
	legacy := filepath.Join(dir, "legacy")
	if got := toolBinary(preferred, legacy); got != legacy {
		t.Fatalf("selected %q, want legacy %q", got, legacy)
	}
}

func TestBrokenPrivateInstallationDoesNotSilentlyUseSharedTool(t *testing.T) {
	dir := t.TempDir()
	preferred := filepath.Join(dir, "private-link")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), preferred); err != nil {
		t.Fatal(err)
	}
	if got := toolBinary(preferred, "shared-tool"); got != preferred {
		t.Fatalf("broken private tool silently fell back to %q", got)
	}
}

func TestPrivateInstallationMayExplicitlyLinkExternalTool(t *testing.T) {
	dir := t.TempDir()
	preferred, external := filepath.Join(dir, "private-link"), filepath.Join(dir, "external")
	if err := os.WriteFile(external, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, preferred); err != nil {
		t.Fatal(err)
	}
	if got := toolBinary(preferred, "legacy"); got != preferred {
		t.Fatalf("explicit external link not selected: %q", got)
	}
}
