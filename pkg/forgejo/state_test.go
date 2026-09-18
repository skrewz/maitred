package forgejo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestKey_String(t *testing.T) {
	k := Key{Repo: "acme/maitred", Kind: KindPR, Number: 12}
	if got := k.String(); got != "acme/maitred/pr/12" {
		t.Errorf("String() = %q, want %q", got, "acme/maitred/pr/12")
	}
}

func TestKey_FileName(t *testing.T) {
	tests := []struct {
		key  Key
		want string
	}{
		{Key{Repo: "acme/maitred", Kind: KindIssue, Number: 49}, "acme__maitred.issue.49.json"},
		{Key{Repo: "acme/maitred", Kind: KindPR, Number: 12}, "acme__maitred.pr.12.json"},
		{Key{Repo: "other", Kind: KindIssue, Number: 1}, "other.issue.1.json"},
	}
	for _, tt := range tests {
		if got := tt.key.fileName(); got != tt.want {
			t.Errorf("%v fileName() = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestStore_NewStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "forgejoeng")
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
}

func TestStore_NewStore_DirectoryError(t *testing.T) {
	// A regular file where the directory must go: MkdirAll fails.
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := NewStore(filepath.Join(file, "sub")); err == nil {
		t.Error("NewStore over a regular file: want an error, got nil")
	}
}

func TestStore_SaveLoad_RoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	issueKey := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 49}
	issueWM := Watermark{Action: "implement", Revision: "2026-09-16T13:25:18Z"}
	if err := s.Save(issueKey, issueWM); err != nil {
		t.Fatalf("Save(issue): %v", err)
	}
	got, err := s.Load(issueKey)
	if err != nil {
		t.Fatalf("Load(issue): %v", err)
	}
	if *got != issueWM {
		t.Errorf("issue watermark = %+v, want %+v", *got, issueWM)
	}

	prKey := Key{Repo: "acme/maitred", Kind: KindPR, Number: 12}
	prWM := Watermark{Action: "review", Revision: "0123456789abcdef0123456789abcdef01234567"}
	if err := s.Save(prKey, prWM); err != nil {
		t.Fatalf("Save(pr): %v", err)
	}
	got, err = s.Load(prKey)
	if err != nil {
		t.Fatalf("Load(pr): %v", err)
	}
	if *got != prWM {
		t.Errorf("pr watermark = %+v, want %+v", *got, prWM)
	}
}

func TestStore_Load_MissingKey(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, err := s.Load(Key{Repo: "acme/maitred", Kind: KindIssue, Number: 1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("Load(missing) = %+v, want nil", *got)
	}
}

func TestStore_Save_Overwrites(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindPR, Number: 7}
	first := Watermark{Action: "review", Revision: "aaa"}
	second := Watermark{Action: "re-review", Revision: "bbb"}
	if err := s.Save(k, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := s.Save(k, second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	got, err := s.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != second {
		t.Errorf("Load = %+v, want %+v", *got, second)
	}
}

func TestStore_RestartSurvival(t *testing.T) {
	dir := t.TempDir()

	// First instance: save watermarks, then discard the handle.
	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	want := map[Key]Watermark{
		{Repo: "acme/maitred", Kind: KindIssue, Number: 49}: {Action: "implement", Revision: "2026-09-16T13:25:18Z"},
		{Repo: "acme/maitred", Kind: KindPR, Number: 12}:    {Action: "review", Revision: "0123456789abcdef0123456789abcdef01234567"},
	}
	for k, w := range want {
		if err := s1.Save(k, w); err != nil {
			t.Fatalf("Save(%v): %v", k, err)
		}
	}
	s1 = nil // simulate the maitred process exiting

	// Second instance: a fresh store on the same directory sees them.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	for k, w := range want {
		got, err := s2.Load(k)
		if err != nil {
			t.Fatalf("Load(%v): %v", k, err)
		}
		if *got != w {
			t.Errorf("Load(%v) = %+v, want %+v", k, *got, w)
		}
	}
}

// TestStore_AtomicWrite_ConcurrentSaves hammers one key from many
// goroutines. Because writes are atomic (temp file + rename), the file on
// disk must always parse as a complete watermark, and no temp files may be
// left behind (§forgejo/state/storage).
func TestStore_AtomicWrite_ConcurrentSaves(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindPR, Number: 1}

	const (
		goroutines = 8
		iterations = 25
	)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				wm := Watermark{
					Action:   "review",
					Revision: strconv.Itoa(g*1000 + i),
				}
				if err := s.Save(k, wm); err != nil {
					t.Errorf("Save: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	got, err := s.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Action != "review" {
		t.Errorf("Action = %q, want %q", got.Action, "review")
	}
	if _, err := strconv.Atoi(got.Revision); err != nil {
		t.Errorf("Revision = %q: not one of the written values (torn write?)", got.Revision)
	}

	// The on-disk file must also be complete JSON.
	data, err := os.ReadFile(filepath.Join(s.dir, k.fileName()))
	if err != nil {
		t.Fatalf("read on-disk file: %v", err)
	}
	var onDisk Watermark
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("on-disk file is not complete JSON: %v", err)
	}

	// No temp files may remain.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != k.fileName() {
			t.Errorf("unexpected file %q left in store directory", e.Name())
		}
	}
}

// TestStore_ConcurrentMixedKeys exercises the per-key locks with saves and
// loads racing across different keys (§forgejo/state/concurrency). Run
// under the race detector.
func TestStore_ConcurrentMixedKeys(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: i}
			for j := 0; j < 20; j++ {
				wm := Watermark{Action: "implement", Revision: strconv.Itoa(j)}
				if err := s.Save(k, wm); err != nil {
					t.Errorf("Save: %v", err)
					return
				}
				if _, err := s.Load(k); err != nil {
					t.Errorf("Load: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: i}
		got, err := s.Load(k)
		if err != nil {
			t.Fatalf("Load(%v): %v", k, err)
		}
		if got == nil {
			t.Fatalf("Load(%v) = nil, want a watermark", k)
		}
	}
}

func TestStore_Load_CorruptFile(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 3}
	if err := os.WriteFile(filepath.Join(s.dir, k.fileName()), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	if _, err := s.Load(k); err == nil {
		t.Error("Load(corrupt) = nil error, want an error")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(corrupt) = %v, want a parse error, not ErrNotExist", err)
	}
}

func TestStore_Save_RenameError(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 5}
	// A directory at the destination makes the rename fail.
	if err := os.Mkdir(filepath.Join(s.dir, k.fileName()), 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if err := s.Save(k, Watermark{Action: "implement", Revision: "x"}); err == nil {
		t.Error("Save over a directory destination: want an error, got nil")
	}
	// The temp file must not be left behind.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != k.fileName() {
		t.Errorf("store directory entries = %v, want only %q", entries, k.fileName())
	}
}

func TestStore_Save_TempFileError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// A read-only directory makes CreateTemp fail.
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o755) })
	if err := s.Save(Key{Repo: "acme/maitred", Kind: KindIssue, Number: 6}, Watermark{Action: "implement", Revision: "x"}); err == nil {
		t.Error("Save in a read-only directory: want an error, got nil")
	}
}

func TestStore_Load_ReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 8}
	if err := s.Save(k, Watermark{Action: "implement", Revision: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// An unreadable file is a read error, not a missing key.
	if err := os.Chmod(filepath.Join(s.dir, k.fileName()), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(s.dir, k.fileName()), 0o644) })
	got, err := s.Load(k)
	if err == nil {
		t.Errorf("Load(unreadable) = %+v, nil error; want an error", got)
	} else if errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(unreadable) = %v, want a read error, not ErrNotExist", err)
	}
}

func TestStore_Save_WritesExpectedJSON(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindPR, Number: 12}
	wm := Watermark{Action: "review", Revision: "0123456789abcdef0123456789abcdef01234567"}
	if err := s.Save(k, wm); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(s.dir, k.fileName()))
	if err != nil {
		t.Fatalf("read on-disk file: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("on-disk file is not JSON: %v", err)
	}
	if fields["action"] != "review" || fields["revision"] != wm.Revision {
		t.Errorf("on-disk JSON = %v, want action=review revision=%s", fields, wm.Revision)
	}
}

func TestStore_WithKeyLock_SerialisesSameKey(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 1}

	var mu sync.Mutex
	cur, maxConc := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.WithKeyLock(k, func() {
				mu.Lock()
				cur++
				if cur > maxConc {
					maxConc = cur
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				cur--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	if maxConc != 1 {
		t.Errorf("max concurrency = %d, want 1 (same key serialised)", maxConc)
	}
}

func TestStore_WithKeyLock_ParallelDifferentKeys(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k1 := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 1}
	k2 := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 2}

	var mu sync.Mutex
	cur, maxConc := 0, 0
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, k := range []Key{k1, k2} {
		wg.Add(1)
		go func(k Key) {
			defer wg.Done()
			<-start
			s.WithKeyLock(k, func() {
				mu.Lock()
				cur++
				if cur > maxConc {
					maxConc = cur
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				cur--
				mu.Unlock()
			})
		}(k)
	}
	close(start)
	wg.Wait()
	if maxConc != 2 {
		t.Errorf("max concurrency = %d, want 2 (different keys in parallel)", maxConc)
	}
}
