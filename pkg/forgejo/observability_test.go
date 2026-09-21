package forgejo

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newEngineWithLogger builds an engine over a fresh fake API, queue, and
// store with the given logger.
func newEngineWithLogger(t *testing.T, logger *slog.Logger) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, logger)
	return eng, api, q, store
}

// parseLogLines decodes every JSON log line in data into a field map
// (§forgejo/observability/the-decision-log).
func parseLogLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not structured JSON: %v\nline: %s", err, line)
		}
		entries = append(entries, m)
	}
	return entries
}

// findEntry returns the first log entry with msg.
func findEntry(t *testing.T, entries []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, e := range entries {
		if e["msg"] == msg {
			return e
		}
	}
	t.Fatalf("no log entry with msg %q in %v", msg, entries)
	return nil
}

// TestHandleEvent_StructuredDecisionLog asserts that a dispatch decision is
// logged as a structured entry with the key, event, decision, action,
// reason, and revision as discrete fields
// (§forgejo/observability/the-decision-log).
func TestHandleEvent_StructuredDecisionLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	eng, api, _, _ := newEngineWithLogger(t, logger)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	e := findEntry(t, parseLogLines(t, buf.Bytes()), "decision")
	if got := e["key"]; got != "o/r/issue/7" {
		t.Errorf("key = %v, want o/r/issue/7", got)
	}
	if got := e["event"]; got != string(EventIssueOpened) {
		t.Errorf("event = %v, want %s", got, EventIssueOpened)
	}
	if got := e["dispatched"]; got != true {
		t.Errorf("dispatched = %v, want true", got)
	}
	if got := e["action"]; got != string(ActionImplement) {
		t.Errorf("action = %v, want implement", got)
	}
	if got := e["reason"]; got != "issue 7 opened: open, unblocked, no connected PR" {
		t.Errorf("reason = %v", got)
	}
	if got := e["revision"]; got != testUpdatedAt.UTC().Format(time.RFC3339) {
		t.Errorf("revision = %v, want %s", got, testUpdatedAt.UTC().Format(time.RFC3339))
	}
}

// TestHandleEvent_StructuredDecisionLog_HoldOff asserts that a hold-off
// decision is logged with dispatched=false and no action
// (§forgejo/observability/the-decision-log).
func TestHandleEvent_StructuredDecisionLog_HoldOff(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	eng, api, _, _ := newEngineWithLogger(t, logger)
	api.issues[7] = testIssue(7, "open")
	api.deps[7] = []Issue{{Number: 3, State: "open", Repository: "o/r"}}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	e := findEntry(t, parseLogLines(t, buf.Bytes()), "decision")
	if got := e["key"]; got != "o/r/issue/7" {
		t.Errorf("key = %v, want o/r/issue/7", got)
	}
	if got := e["dispatched"]; got != false {
		t.Errorf("dispatched = %v, want false", got)
	}
	if got := e["action"]; got != "" {
		t.Errorf("action = %v, want empty", got)
	}
	if got, _ := e["reason"].(string); !strings.Contains(got, "blocked by 1 open issue(s)") {
		t.Errorf("reason = %v, want it to mention the open blocker", got)
	}
}

// TestTracked_Empty: a fresh engine tracks nothing
// (§forgejo/observability/the-dashboard-view).
func TestTracked_Empty(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	if got := eng.Tracked(); len(got) != 0 {
		t.Fatalf("Tracked() = %v, want empty", got)
	}
}

// TestTracked_AfterDispatch: the view shows the current state, the
// watermark, and the last decision for a dispatched issue
// (§forgejo/observability/the-dashboard-view).
func TestTracked_AfterDispatch(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	tracked := eng.Tracked()
	if len(tracked) != 1 {
		t.Fatalf("Tracked() has %d entries, want 1", len(tracked))
	}
	e := tracked[0]
	if e.Repo != "o/r" || e.Kind != KindIssue || e.Number != 7 {
		t.Errorf("key = %v/%v/%d, want o/r/issue/7", e.Repo, e.Kind, e.Number)
	}
	if e.State != "open" {
		t.Errorf("state = %q, want open", e.State)
	}
	if e.Revision != testUpdatedAt.UTC().Format(time.RFC3339) {
		t.Errorf("revision = %q", e.Revision)
	}
	if e.Watermark == nil || e.Watermark.Action != string(ActionImplement) || e.Watermark.Revision != e.Revision {
		t.Errorf("watermark = %+v, want implement at the issue revision", e.Watermark)
	}
	if !e.Decision.Dispatched || e.Decision.Action != ActionImplement {
		t.Errorf("decision = %+v, want a dispatched implement", e.Decision)
	}
	if e.Decision.Event != EventIssueOpened {
		t.Errorf("decision event = %v, want %s", e.Decision.Event, EventIssueOpened)
	}
	if e.Decision.Reason == "" {
		t.Error("decision has no reason")
	}
	if e.Decision.At.IsZero() {
		t.Error("decision has no timestamp")
	}
}

// TestTracked_HoldOffRecorded: a hold-off is tracked with no watermark
// (§forgejo/observability/the-dashboard-view).
func TestTracked_HoldOffRecorded(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")
	api.deps[7] = []Issue{{Number: 3, State: "open", Repository: "o/r"}}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	tracked := eng.Tracked()
	if len(tracked) != 1 {
		t.Fatalf("Tracked() has %d entries, want 1", len(tracked))
	}
	e := tracked[0]
	if e.Watermark != nil {
		t.Errorf("watermark = %+v, want nil (nothing dispatched)", e.Watermark)
	}
	if e.Decision.Dispatched {
		t.Errorf("decision = %+v, want a hold-off", e.Decision)
	}
	if e.Decision.Reason == "" {
		t.Error("decision has no reason")
	}
}

// TestTracked_MergedPR: a merged pull request is tracked with its merged
// state (§forgejo/observability/the-dashboard-view).
func TestTracked_MergedPR(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.pulls[9] = &PullRequest{
		Number:  9,
		State:   "closed",
		Merged:  true,
		HeadSHA: "sha-9",
	}

	eng.HandleEvent(Event{Type: EventPRClosed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"})

	tracked := eng.Tracked()
	if len(tracked) != 1 {
		t.Fatalf("Tracked() has %d entries, want 1", len(tracked))
	}
	e := tracked[0]
	if e.Kind != KindPR || e.Number != 9 {
		t.Errorf("key = %v, want o/r/pr/9", e.Key)
	}
	if e.State != "closed" || !e.Merged {
		t.Errorf("state = %q merged = %v, want closed/merged", e.State, e.Merged)
	}
}

// TestTracked_CorruptWatermark: a corrupt on-disk watermark is treated
// as absent in the view (lossiness, §forgejo/state/lossiness).
func TestTracked_CorruptWatermark(t *testing.T) {
	eng, api, _, store := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	key := Key{Repo: "o/r", Kind: KindIssue, Number: 7}
	if err := os.WriteFile(filepath.Join(store.dir, key.fileName()), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt watermark: %v", err)
	}

	tracked := eng.Tracked()
	if len(tracked) != 1 {
		t.Fatalf("Tracked() has %d entries, want 1", len(tracked))
	}
	if tracked[0].Watermark != nil {
		t.Errorf("watermark = %+v, want nil (corrupt watermark treated as lost)", tracked[0].Watermark)
	}
}

// TestTracked_Sorted: the view is deterministic — sorted by repo, kind,
// number (§forgejo/observability/the-dashboard-view).
func TestTracked_Sorted(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")
	api.issues[3] = testIssue(3, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})
	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 3, Sender: "alice"})

	tracked := eng.Tracked()
	if len(tracked) != 2 {
		t.Fatalf("Tracked() has %d entries, want 2", len(tracked))
	}
	if tracked[0].Number != 3 || tracked[1].Number != 7 {
		t.Errorf("entries not sorted: %d, %d", tracked[0].Number, tracked[1].Number)
	}
}
