package forgejo

import (
	"sort"
	"time"
)

// DecisionRecord is the engine's in-memory record of the last decision
// for one key, with the re-fetched context the dashboard view shows
// (§forgejo/observability/the-dashboard-view).
type DecisionRecord struct {
	// Event is the event type that produced the decision.
	Event EventType `json:"event"`
	// Dispatched reports whether the decision dispatched an action.
	Dispatched bool `json:"dispatched"`
	// Action is the dispatched action (meaningful when Dispatched).
	Action Action `json:"action,omitempty"`
	// Reason is the decision's human-readable reason.
	Reason string `json:"reason"`
	// State is the object's state as re-fetched for the decision
	// ("open"/"closed").
	State string `json:"state,omitempty"`
	// Merged reports whether the re-fetched object is a merged pull
	// request.
	Merged bool `json:"merged,omitempty"`
	// Revision is the revision the decision was made at: the PR head
	// sha, or the issue's updated_at.
	Revision string `json:"revision,omitempty"`
	// At is when the decision was made.
	At time.Time `json:"at"`
}

// Tracked is one row of the dashboard view: a tracked issue or pull
// request with its current state, watermark, and last decision
// (§forgejo/observability/the-dashboard-view).
type Tracked struct {
	// Key identifies the tracked object.
	Key
	// State is the object's state as last re-fetched ("open"/"closed").
	State string `json:"state"`
	// Merged reports whether the object is a merged pull request.
	Merged bool `json:"merged,omitempty"`
	// Revision is the revision the last decision was made at.
	Revision string `json:"revision,omitempty"`
	// Watermark is the last dispatch recorded for the key, or nil when
	// nothing has been dispatched.
	Watermark *Watermark `json:"watermark,omitempty"`
	// Decision is the last decision made for the key.
	Decision DecisionRecord `json:"decision"`
}

// recordDecision records the decision for the key in the in-memory
// decision log and emits the structured decision log entry — the
// dispatched action plus its reason, or the hold-off plus its reason,
// keyed by repo/issue/PR (§forgejo/observability/the-decision-log,
// §forgejo/observability/the-dashboard-view).
func (e *Engine) recordDecision(k Key, ev Event, st State, d Decision) {
	rec := DecisionRecord{
		Event:      ev.Type,
		Dispatched: d.Dispatched,
		Action:     d.Action,
		Reason:     d.Reason,
		Revision:   revisionFor(ev, st),
		At:         time.Now(),
	}
	switch {
	case st.Issue != nil:
		rec.State = st.Issue.State
	case st.PR != nil:
		rec.State = st.PR.State
		rec.Merged = st.PR.Merged
	}
	e.decMu.Lock()
	e.decisions[k] = rec
	e.decMu.Unlock()

	e.log.Info("decision",
		"key", k.String(),
		"event", string(ev.Type),
		"dispatched", d.Dispatched,
		"action", string(d.Action),
		"reason", d.Reason,
		"revision", rec.Revision,
	)
}

// Tracked returns the dashboard view: the issues and pull requests the
// engine has made a decision on, each with its current state, watermark,
// and last decision, sorted by repo, kind, and number
// (§forgejo/observability/the-dashboard-view). A watermark that cannot
// be read is treated as absent (lossiness, §forgejo/state/lossiness).
func (e *Engine) Tracked() []Tracked {
	e.decMu.Lock()
	keys := make([]Key, 0, len(e.decisions))
	for k := range e.decisions {
		keys = append(keys, k)
	}
	e.decMu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Repo != keys[j].Repo {
			return keys[i].Repo < keys[j].Repo
		}
		if keys[i].Kind != keys[j].Kind {
			return keys[i].Kind < keys[j].Kind
		}
		return keys[i].Number < keys[j].Number
	})

	tracked := make([]Tracked, 0, len(keys))
	for i := range keys {
		k := keys[i]
		e.decMu.Lock()
		rec := e.decisions[k]
		e.decMu.Unlock()
		t := Tracked{
			Key:      k,
			State:    rec.State,
			Merged:   rec.Merged,
			Revision: rec.Revision,
			Decision: rec,
		}
		if w, err := e.store.Load(k); err != nil {
			e.log.Warn("load watermark (view)", "key", k.String(), "error", err)
		} else {
			t.Watermark = w
		}
		tracked = append(tracked, t)
	}
	return tracked
}
