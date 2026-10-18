package main

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestIndependentFDReviewCleanupFailureBlocksReuse(t *testing.T) {
    root := t.TempDir()
    marker := filepath.Join(root, "ran")
    script := filepath.Join(root, "child.sh")
    body := "#!/bin/sh\nprintf x >> \"$RELAY_CLEANUP_MARKER\"; printf '{\"bad\":true}' > account.json; printf x > extra\n"
    if err := os.WriteFile(script, []byte(body), 0700); err != nil { t.Fatal(err) }
    t.Setenv("RELAY_CLEANUP_MARKER", marker)
    ctx, cancel := context.WithTimeout(context.Background(), time.Second); defer cancel()
    if _, err := generateRelayAccount(ctx, script, root); err == nil { t.Fatal("cleanup failure unexpectedly succeeded") }
    entries, err := os.ReadDir(root); if err != nil { t.Fatal(err) }
    stages := 0
    for _, e := range entries { if strings.HasPrefix(e.Name(), relayStagePrefix) { stages++ } }
    if stages != 1 { t.Fatalf("want one retained stage after cleanup failure, got %d", stages) }
    if _, err := generateRelayAccount(ctx, script, root); err == nil { t.Fatal("reuse unexpectedly succeeded with leftover stage") }
    b, err := os.ReadFile(marker); if err != nil { t.Fatal(err) }
    if string(b) != "x" { t.Fatalf("second worker ran despite blocked reservation: %q", b) }
}
