package main

import (
	"os"
	"path/filepath"
	"testing"
)

// skipUnlessAccessDenied is the same probe pkg/forgejo uses for its
// permission-failure tests: it skips the calling test when a mode of
// 0o000 denies this process nothing. The suite usually runs as uid 0 in
// CI containers, where the chmod succeeds and the read afterwards still
// works, so the test would assert a failure it cannot cause. It is
// repeated here only because a test helper cannot cross a package
// boundary.
func skipUnlessAccessDenied(t *testing.T) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "access.probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o600); err != nil {
		t.Fatalf("write access probe: %v", err)
	}
	if err := os.Chmod(probe, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	if _, err := os.ReadFile(probe); err == nil {
		t.Skip("a 0o000 file is still readable by this process: the permission failure this test asserts cannot be produced")
	}
}
