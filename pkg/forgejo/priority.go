package forgejo

import (
	"strconv"
)

// Task priority: the tier forgejoeng asks the queue to serve a dispatch at.
//
// The queue owns the vocabulary and maitred forwards it verbatim
// (§overview/scope). forgejoeng names one tier of its own — the one
// the `vip` label asks for — and everywhere else leaves the priority unset,
// so the queue's default tier applies as it did before the field existed
// (§forgejo/webhook/vip-priority).

// PriorityTeacher is the queue tier a vip dispatch asks for: the queue's own
// word, forwarded verbatim, the middle of the three tiers hotelier
// recognises (§forgejo/webhook/vip-priority).
const PriorityTeacher = "teacher"

// taskPriority is the priority a dispatch is scheduled at: the teacher tier
// where the `vip` label stands behind the object — on it, on the issue a
// pull request delivers, or on the tracker of the forest an issue belongs to
// — and unset elsewhere (§forgejo/webhook/vip-priority).
//
// The label is judged at the moment of scheduling and read fresh on every
// dispatch: nothing is cached in engine state, so attaching `vip` lifts the
// next scheduling and removing it lowers that scheduling, with no
// retroactive change to anything already queued.
//
// The walk is a ladder, not a survey: it stops at the first carrier.
// When no carrier wears the label it pays no read the dispatch's own
// re-fetch did not make — an object wearing the label costs no read at
// all, and a forest nobody has labelled vip costs no hop either: the
// label is what opens the graph, so the common dispatch is exactly as
// cheap as it was before the field existed. A labelled tracker in the
// rows costs one hop — the tracker's gate — per dispatch, and the
// review fan-out pays it once per lens; the cascade's issue leg reads
// the issue's blocker rows once, being the one path that holds none
// (§forgejo/decisions/unblock-cascade). A failed read costs the tier
// and not the dispatch — an object whose forest cannot be read is
// scheduled at the default like any unlabelled object, with one warn
// line naming the failure — because the label is an annotation on a
// dispatch that stands on its own, and holding the work back over it
// would make the queue poorer, not safer
// (§forgejo/webhook/the-pipeline, §forgejo/webhook/vip-priority).
func (e *Engine) taskPriority(st State) string {
	if e.vipObject(st) {
		return PriorityTeacher
	}
	return ""
}

// vipObject reports whether the vip label stands behind a dispatch: on the
// pull request being scheduled, on an issue it delivers, or on the tracker of
// a forest such an issue belongs to (§forgejo/webhook/vip-priority). The
// delivery relation is the one the merge gate reads — the issues a pull
// request's body auto-closes and those its blocking edges name
// (§forgejo/decisions/merge-gate) — so a pull request derived from a vip
// issue is vip whatever path scheduled it. A state carrying neither object —
// the housekeeping row's — has nothing to judge.
func (e *Engine) vipObject(st State) bool {
	if st.PR != nil {
		if carriesLabel(st.PR.Labels, LabelVIP) {
			return true
		}
		for i := range st.ConnectedIssues {
			if e.vipIssue(&st.ConnectedIssues[i].Issue, st.ConnectedIssues[i].Blockers) {
				return true
			}
		}
		return false
	}
	if st.Issue != nil {
		return e.vipIssue(st.Issue, st.Blockers)
	}
	return false
}

// vipIssue reports whether the vip label stands behind an issue: on the
// issue itself, or on the tracker of the forest it belongs to
// (§forgejo/webhook/vip-priority).
//
// A forest's tracker is the `outcome` issue blocked by the `ideation` root
// that gates the forest (§forgejo/decisions/forest-teardown,
// §forgejo/reconciliation/watermark-exemptions), so the tracker is read from
// an issue's blocker rows — the side of the graph a dispatch nearly always
// holds already, and the only hop the walk makes. Two shapes carry the label
// into an issue's scheduling:
//
//   - the issue wears it, and the dispatch is the issue's own;
//   - the issue is blocked by a vip tracker — an `outcome` issue wearing the
//     label and gated in turn by an `ideation` root, which is what makes it
//     a forest's tracker rather than an outcome issue that happens to wear a
//     label. The member and the tracker need not share the root directly:
//     the tracker blocking the member is the membership that matters.
//
// A tracker's own dispatch needs no second shape — its own label answers
// at the first check, no walk taken; the shape above is what makes it the
// tracker of the forest rather than a labelled outcome issue.
// The rows are those the dispatch assembled: the event and sweep paths
// read an issue's blockers as part of the re-fetch
// (§forgejo/webhook/re-fetch), and the unblock cascade's issue leg — the
// one path that holds none — reads them once per dispatch, so the tracker
// carrier is judged there too (§forgejo/decisions/unblock-cascade).
func (e *Engine) vipIssue(issue *Issue, blockers []Issue) bool {
	if carriesLabel(issue.Labels, LabelVIP) {
		return true
	}
	for _, blocker := range blockers {
		if blocker.IsPull || !hasLabel(&blocker, LabelOutcome) || !carriesLabel(blocker.Labels, LabelVIP) {
			continue // neither a forest's gate nor a labelled tracker
		}
		if e.vipGatedByRoot(blocker) {
			return true
		}
	}
	return false
}

// vipGatedByRoot reports whether an issue is gated by an `ideation` root —
// the edge that makes an `outcome` issue *the* tracker of a forest rather
// than an outcome issue that happens to wear a label
// (§forgejo/webhook/vip-priority). The read is made in the candidate row's
// own repository, so a tracker across the organisation is still read where
// it lives; a row that names none names no forest either, and the walk pays
// a hop it cannot answer (§forgejo/client/operations).
func (e *Engine) vipGatedByRoot(candidate Issue) bool {
	repo := candidate.Repository
	if repo == "" {
		return false // a row that names no repository names no forest
	}
	owner, name := splitRepo(repo)
	gates, err := e.api.IssueDependencies(owner, name, candidate.Number)
	if err != nil {
		// The degraded continuations elsewhere in this package are loud
		// for the same reason: a fallback nobody can see is a fallback
		// nobody can explain (§forgejo/webhook/vip-priority).
		e.log.Warn("vip walk: issue-dependencies read failed; scheduling at the queue's default tier",
			"object", repo+"#"+strconv.Itoa(candidate.Number), "error", err)
		return false
	}
	// The same gate the commissioning rule reads — what makes the
	// candidate a tracker is the ideation root blocking it, whatever
	// that root's own state: the walk asks whether this is the forest's
	// tracker, not whether the forest still stands
	// (§forgejo/decisions/commissioning).
	return ideationGate(gates) != nil
}
