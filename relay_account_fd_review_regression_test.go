package main

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestIndependentFDReviewPrivateProbe(t *testing.T) {
    control := t.TempDir()
    root := filepath.Join(control, "root")
    moved := filepath.Join(control, "moved")
    if err := os.Mkdir(root, 0700); err != nil { t.Fatal(err) }
    script := filepath.Join(control, "child.sh")
    body := "#!/bin/sh\nprintf 'probe-secret-stdout\\n'; printf 'probe-secret-stderr\\n' >&2; printf x > \"$RELAY_PROBE_CONTROL/ready\"; while [ ! -e \"$RELAY_PROBE_CONTROL/release\" ]; do sleep 0.001; done; printf '{\"private_key\":\"bad\"}' > account.json\n"
    if err := os.WriteFile(script, []byte(body), 0700); err != nil { t.Fatal(err) }
    t.Setenv("RELAY_PROBE_CONTROL", control)
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second); defer cancel()
    done := make(chan error, 1)
    go func() { _, err := generateRelayAccount(ctx, script, root); done <- err }()
    deadline := time.Now().Add(time.Second)
    for {
        if _, err := os.Stat(filepath.Join(control, "ready")); err == nil { break }
        if time.Now().After(deadline) { t.Fatal("child did not start") }
        time.Sleep(time.Millisecond)
    }
    if err := os.Rename(root, moved); err != nil { t.Fatal(err) }
    if err := os.Mkdir(root, 0700); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(control, "release"), []byte("x"), 0600); err != nil { t.Fatal(err) }
    if err := <-done; err == nil { t.Fatal("invalid synthetic output unexpectedly accepted") }
    entries, err := os.ReadDir(moved); if err != nil { t.Fatal(err) }
    for _, e := range entries { if strings.HasPrefix(e.Name(), relayStagePrefix) { t.Fatalf("leftover stage %s", e.Name()) } }
}
