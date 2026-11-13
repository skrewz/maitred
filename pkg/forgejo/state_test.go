package forgejo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
		{Key{Repo: "acme/maitred", Kind: KindForest, Number: 49}, "acme__maitred.forest.49.json"},
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

// watermarksEqual compares two watermarks; the record gained a map
// (the per-subject slots) and lost struct comparability, so the tests
// compare through DeepEqual (§forgejo/state/watermark).
func watermarksEqual(a, b Watermark) bool {
	return reflect.DeepEqual(a, b) || (a.Action == b.Action && a.Revision == b.Revision && len(a.Subjects) == 0 && len(b.Subjects) == 0)
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
	if !watermarksEqual(*got, issueWM) {
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
	if !watermarksEqual(*got, prWM) {
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
	second := Watermark{Action: "review", Revision: "bbb"}
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
	if !watermarksEqual(*got, second) {
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
		if !watermarksEqual(*got, w) {
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

// TestWatermark_SubjectSlots: the per-subject slots round-trip through
// storage — one file per object still, the lens slots surviving a
// restart — and a watermark written before the lens list loads with
// empty slots (§forgejo/state/watermark, §forgejo/state/persistence).
func TestWatermark_SubjectSlots(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := Key{Repo: "acme/widgets", Kind: KindPR, Number: 3}
	w := Watermark{
		Action:   "fix-feedback",
		Revision: "sha-9",
		Subjects: map[string]Slot{
			"lens-a": {Action: "review", Revision: "sha-9"},
			"lens-b": {Action: "review", Revision: "sha-9"},
		},
	}
	if err := s.Save(k, w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("store holds %d files for one object, want 1", len(entries))
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	got, err := s2.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !watermarksEqual(*got, w) {
		t.Errorf("round trip = %+v, want %+v", *got, w)
	}

	// A pre-lens watermark file — main slot only — loads with no
	// subjects; the fan-out guard reads it as "never dispatched"
	// (§forgejo/state/lossiness).
	legacy := Key{Repo: "acme/widgets", Kind: KindPR, Number: 4}
	if err := os.WriteFile(filepath.Join(dir, legacy.fileName()),
		[]byte(`{"action":"review","revision":"sha-1"}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	got, err = s2.Load(legacy)
	if err != nil {
		t.Fatalf("Load(legacy): %v", err)
	}
	if got.Action != "review" || got.Revision != "sha-1" || len(got.Subjects) != 0 {
		t.Errorf("legacy watermark = %+v, want the main slot only", *got)
	}
}

// TestForestKey: the forest slot is keyed off the ideation root under
// the forest kind, so the decompose comparison finds it and it never
// shares a file with the root's own watermark
// (§forgejo/state/forest-digest).
func TestForestKey(t *testing.T) {
	root := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 49}
	forest := ForestKey("acme/maitred", 49)
	if forest.Repo != root.Repo || forest.Number != root.Number || forest.Kind != KindForest {
		t.Errorf("ForestKey = %+v, want the root's repo and number under kind %q", forest, KindForest)
	}
	if got, want := forest.String(), "acme/maitred/forest/49"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if forest.fileName() == root.fileName() {
		t.Error("forest slot shares a file with the root issue's watermark")
	}
}

// TestStore_ForestDigest_RoundTrip: a digest saved into the forest's
// slot and reloaded is byte-identical, and the on-disk record carries
// it in the digest field (§forgejo/state/forest-digest).
func TestStore_ForestDigest_RoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := ForestKey("acme/maitred", 49)
	digest := "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
	if err := s.Save(k, Watermark{Digest: digest}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || got.Digest != digest {
		t.Errorf("loaded digest = %q, want %q (byte-identical)", got, digest)
	}

	data, err := os.ReadFile(filepath.Join(s.dir, k.fileName()))
	if err != nil {
		t.Fatalf("read on-disk file: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("on-disk file is not JSON: %v", err)
	}
	if fields["digest"] != digest {
		t.Errorf("on-disk JSON = %v, want digest=%s", fields, digest)
	}
}

// TestStore_ForestDigest_RestartSurvival: a store newly opened on the
// same directory sees the forest digests a previous instance saved — a
// quiet forest stays quiet across restarts
// (§forgejo/state/forest-digest, §forgejo/state/persistence).
func TestStore_ForestDigest_RestartSurvival(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	digests := map[string]string{
		"acme/maitred#49": "aaaa2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266",
		"acme/other#7":    "bbbb2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266",
	}
	for ref, digest := range digests {
		repo, numberStr, ok := strings.Cut(ref, "#")
		if !ok {
			t.Fatalf("bad ref %q", ref)
		}
		number, err := strconv.Atoi(numberStr)
		if err != nil {
			t.Fatalf("bad number in %q: %v", ref, err)
		}
		if err := s1.Save(ForestKey(repo, number), Watermark{Digest: digest}); err != nil {
			t.Fatalf("Save(%s): %v", ref, err)
		}
	}
	s1 = nil // simulate the maitred process exiting

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	for ref, digest := range digests {
		repo, numberStr, _ := strings.Cut(ref, "#")
		number, err := strconv.Atoi(numberStr)
		if err != nil {
			t.Fatalf("bad number in %q: %v", ref, err)
		}
		got, err := s2.Load(ForestKey(repo, number))
		if err != nil {
			t.Fatalf("Load(%s): %v", ref, err)
		}
		if got == nil || got.Digest != digest {
			t.Errorf("after restart %s digest = %+v, want %q intact", ref, got, digest)
		}
	}
}

// TestStore_ForestDigest_ConcurrentEvaluations: concurrent evaluations
// for one forest serialise through the key lock — a
// load-compare-refresh under WithKeyLock never interleaves, and exactly
// one digest stands at the end (§forgejo/state/forest-digest,
// §forgejo/state/concurrency).
func TestStore_ForestDigest_ConcurrentEvaluations(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := ForestKey("acme/maitred", 49)

	const n = 10
	var mu sync.Mutex
	cur, maxConc := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.WithKeyLock(k, func() {
				mu.Lock()
				cur++
				if cur > maxConc {
					maxConc = cur
				}
				mu.Unlock()

				// The evaluation's critical section: read the stored
				// digest, then refresh it. Under the lock the value
				// must not move between the two reads — no other
				// evaluation's refresh may interleave them.
				w, err := s.loadLocked(k)
				if err != nil {
					t.Errorf("loadLocked: %v", err)
				}
				var before string
				if w != nil {
					before = w.Digest
				}
				time.Sleep(time.Millisecond)
				w2, err := s.loadLocked(k)
				if err != nil {
					t.Errorf("loadLocked: %v", err)
				}
				var during string
				if w2 != nil {
					during = w2.Digest
				}
				if before != during {
					t.Errorf("stored digest moved mid-evaluation from %q to %q: evaluations interleaved", before, during)
				}
				if err := s.saveLocked(k, Watermark{Digest: strconv.Itoa(i)}); err != nil {
					t.Errorf("saveLocked: %v", err)
				}

				mu.Lock()
				cur--
				mu.Unlock()
			})
		}(i)
	}
	wg.Wait()
	if maxConc != 1 {
		t.Errorf("max concurrency = %d, want 1 (same forest serialised)", maxConc)
	}
	got, err := s.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || got.Digest == "" {
		t.Fatalf("final digest = %+v, want one evaluation's refresh", got)
	}
}

// TestStore_ForestDigest_DistinctFromIssueSlot: writing a forest's
// digest leaves the root issue's own watermark untouched and vice
// versa — the forest kind is a separate slot, not a second field the
// dispatch writers would clobber (§forgejo/state/forest-digest).
func TestStore_ForestDigest_DistinctFromIssueSlot(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	root := Key{Repo: "acme/maitred", Kind: KindIssue, Number: 49}
	forest := ForestKey("acme/maitred", 49)

	if err := s.Save(root, Watermark{Action: "wrap-up", Revision: "2026-09-16T13:25:18Z"}); err != nil {
		t.Fatalf("Save(root): %v", err)
	}
	if err := s.Save(forest, Watermark{Digest: "cafe"}); err != nil {
		t.Fatalf("Save(forest): %v", err)
	}
	w, err := s.Load(root)
	if err != nil {
		t.Fatalf("Load(root): %v", err)
	}
	if w.Digest != "" {
		t.Errorf("issue watermark gained a digest field %q", w.Digest)
	}
	f, err := s.Load(forest)
	if err != nil {
		t.Fatalf("Load(forest): %v", err)
	}
	if f.Action != "" || f.Revision != "" || f.Digest != "cafe" {
		t.Errorf("forest slot = %+v, want the digest only", *f)
	}

	// A later dispatch-style write to the issue slot cannot clobber
	// the forest's digest, because the slots are separate files.
	if err := s.Save(root, Watermark{Action: "decompose", Revision: "sha-2"}); err != nil {
		t.Fatalf("Save(root again): %v", err)
	}
	f, err = s.Load(forest)
	if err != nil {
		t.Fatalf("Load(forest): %v", err)
	}
	if f.Digest != "cafe" {
		t.Errorf("forest digest = %q after an issue-slot write, want %q intact", f.Digest, "cafe")
	}
}
