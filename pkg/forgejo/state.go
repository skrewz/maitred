package forgejo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Kind identifies what a watermark tracks.
type Kind string

// The two kinds of tracked object (§forgejo/state/storage).
const (
	KindIssue Kind = "issue"
	KindPR    Kind = "pr"
)

// Key identifies a tracked issue or pull request. It is the watermark
// store's key: repo + kind + number (§forgejo/state/storage).
type Key struct {
	// Repo is the repository's "owner/repo" full name.
	Repo string `json:"repo"`
	// Kind is KindIssue or KindPR.
	Kind Kind `json:"kind"`
	// Number is the issue or pull request number.
	Number int `json:"number"`
}

// String renders the key as "owner/repo/kind/number" for logging and
// diagnostics.
func (k Key) String() string {
	return fmt.Sprintf("%s/%s/%d", k.Repo, k.Kind, k.Number)
}

// fileName returns the JSON file name for this key. The "/" in the repo
// full name is escaped to "__" so every watermark stays a single file in
// the store directory.
func (k Key) fileName() string {
	return strings.ReplaceAll(k.Repo, "/", "__") + "." + string(k.Kind) + "." + strconv.Itoa(k.Number) + ".json"
}

// Slot is one (action, revision) dispatch record: the shape a
// watermark holds per subject of a fan-out dispatch
// (§forgejo/state/watermark).
type Slot struct {
	// Action is the named action dispatched for this subject.
	Action string `json:"action"`
	// Revision is the activity revision the action was dispatched at.
	Revision string `json:"revision"`
}

// Watermark is the "what have I dispatched" record for a single issue or
// pull request (§forgejo/state/watermark). The main (Action, Revision)
// slot records the object's own singleton dispatches; Subjects carries
// one slot per subject of a fan-out dispatch — the review lenses — so
// two lenses dispatched at the same revision do not hold each other
// off as "already dispatched". Storage stays one file per object
// (§forgejo/state/storage).
type Watermark struct {
	// Action is the named action last dispatched for this key (the
	// decision function's action names).
	Action string `json:"action"`
	// Revision is the activity revision the action was dispatched at:
	// the PR head sha for pull requests, the issue's updated_at for
	// issues.
	Revision string `json:"revision"`
	// Subjects maps each fan-out subject's identity (a review lens's
	// configured login) to the slot dispatched for it. Empty for
	// objects that have only ever seen singleton dispatches, and for
	// watermarks written before the lens list (§forgejo/state/watermark).
	Subjects map[string]Slot `json:"subjects,omitempty"`
}

// subjectSlot returns the slot recorded for subject, if any.
func (w *Watermark) subjectSlot(subject string) (Slot, bool) {
	if w == nil || w.Subjects == nil {
		return Slot{}, false
	}
	s, ok := w.Subjects[subject]
	return s, ok
}

// subjectSlots returns a copy of the subject-slot map, nil-safe for a
// missing watermark; the copy keeps a merge-save from aliasing the
// record it was merged into (§forgejo/state/watermark).
func (w *Watermark) subjectSlots() map[string]Slot {
	if w == nil || w.Subjects == nil {
		return nil
	}
	m := make(map[string]Slot, len(w.Subjects))
	for k, v := range w.Subjects {
		m[k] = v
	}
	return m
}

// Store persists watermarks as one JSON file per key under a directory
// (§forgejo/state/storage).
type Store struct {
	dir   string
	mu    sync.Mutex // guards locks
	locks map[string]*sync.Mutex
}

// NewStore creates a watermark store rooted at dir, creating the directory
// (and any parents) if it does not exist. A store newly opened on a
// directory a previous instance used sees the watermarks it saved
// (§forgejo/state/persistence).
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create watermark directory %q: %w", dir, err)
	}
	return &Store{
		dir:   dir,
		locks: make(map[string]*sync.Mutex),
	}, nil
}

// WithKeyLock runs fn while holding the per-key lock for key, so a
// read-decide-update sequence for one key cannot interleave with another
// caller's for the same key (§forgejo/state/concurrency).
func (s *Store) WithKeyLock(k Key, fn func()) {
	l := s.lockFor(k)
	l.Lock()
	defer l.Unlock()
	fn()
}

// lockFor returns the per-key mutex for key, so concurrent updates for the
// same key are serialised while different keys proceed in parallel
// (§forgejo/state/concurrency).
func (s *Store) lockFor(k Key) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[k.String()]
	if !ok {
		l = &sync.Mutex{}
		s.locks[k.String()] = l
	}
	return l
}

// Load returns the watermark for key, or (nil, nil) if none has been saved
// yet. A corrupt on-disk file is reported as an error; treating it as a
// lost watermark is the caller's choice (§forgejo/state/lossiness).
func (s *Store) Load(k Key) (*Watermark, error) {
	l := s.lockFor(k)
	l.Lock()
	defer l.Unlock()
	return s.loadLocked(k)
}

// loadLocked is Load without taking the per-key lock; the caller must
// hold it (e.g. via WithKeyLock).
func (s *Store) loadLocked(k Key) (*Watermark, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, k.fileName()))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read watermark %s: %w", k, err)
	}
	var w Watermark
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parse watermark %s: %w", k, err)
	}
	return &w, nil
}

// Save atomically writes the watermark for key: the JSON is written to a
// temporary file in the store directory and renamed over the destination,
// so a reader never observes a torn or partially written watermark
// (§forgejo/state/storage).
func (s *Store) Save(k Key, w Watermark) error {
	l := s.lockFor(k)
	l.Lock()
	defer l.Unlock()
	return s.saveLocked(k, w)
}

// saveLocked is Save without taking the per-key lock; the caller must
// hold it (e.g. via WithKeyLock).
func (s *Store) saveLocked(k Key, w Watermark) error {
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("marshal watermark %s: %w", k, err)
	}

	tmp, err := os.CreateTemp(s.dir, "."+k.fileName()+".tmp")
	if err != nil {
		return fmt.Errorf("create temp file for watermark %s: %w", k, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed; cleans up on failure

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write watermark %s: %w", k, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close watermark %s: %w", k, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, k.fileName())); err != nil {
		return fmt.Errorf("replace watermark %s: %w", k, err)
	}
	return nil
}
