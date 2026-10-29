package forgejo

// Reserved label names (§forgejo/decisions/actions).
//
// This file is the single home of the engine's reserved label vocabulary:
// one definition per reserved name, referenced wherever the name is
// tested. Reserved names are matched by exact, case-sensitive comparison,
// so a name defined twice is a divergence waiting to happen — add to this
// block instead of declaring the name again.
const (
	// LabelIdeation marks an issue whose forest is not yet commissioned: an
	// ideation issue closes when the human commissions its forest, so an
	// open issue carrying it blocks the work started against it
	// (§forgejo/decisions/commissioning). It also marks a root to
	// decompose, and — seen from a tracker whose copy of that edge is
	// closed — a forest commissioned but not yet finished
	// (§forgejo/reconciliation/watermark-exemptions).
	LabelIdeation = "ideation"
	// LabelOutcome marks an issue as a tracker to wrap up once all
	// its blockers are closed
	// (§forgejo/reconciliation/watermark-exemptions).
	LabelOutcome = "outcome"
	// LabelTriggerReview is the explicit request for an agentic
	// review: an open, mergeable pull request carrying it is reviewed
	// whatever its review state — the label overrides the trigger, not
	// the review's content (§forgejo/decisions/label-triggered-review).
	// Unlike the reserved names above it requests no work item; it
	// asks for a review of one.
	LabelTriggerReview = "trigger-review"
	// LabelTeardownForest orders the decomposer to delete the forest an
	// open ideation root commissioned. It is a modifier of the root row,
	// not a kind label: alone it makes no issue a root, a tracker or a
	// claim, and retires no work item
	// (§forgejo/decisions/forest-teardown).
	LabelTeardownForest = "teardown-forest"
	// LabelSync marks a grouping issue: a sync issue whose blocking
	// issues are the members of a landing set — changes that are not
	// deployable alone and land together, in the order the body names
	// (§forgejo/decisions/sync-set). It is a kind, like ideation and
	// outcome: the issue carrying it is never a work item.
	LabelSync = "sync"
	// LabelAgenticSyncMerge marks a landing-set member: its PR may
	// land automatically as part of the set, and must never be
	// merged by merge-or-wait on its own — landing is the set's
	// decision (§forgejo/decisions/sync-set).
	LabelAgenticSyncMerge = "agentic-sync-merge"
	// LabelAgenticAutoMerge is the work item's opt-in to merging on
	// its own once approved and unblocked. The merge-or-wait prompt's
	// merge policy owns it; the decision function reads it only to
	// name the error state of a landing-set member carrying both
	// opt-ins, which the approval hold-off contains
	// (§forgejo/decisions/sync-set).
	LabelAgenticAutoMerge = "agentic-auto-merge"
	// LabelForestSeeding is the transient hold-off on a newborn forest
	// member: the decomposer attaches it atomically at the member's
	// creation, wires the member's blocking edges, then removes it.
	// While it is present the issue holds off from implement and
	// reassess on every path, so no event can dispatch work before the
	// forest's gate exists. It is not a kind label — the member IS a
	// work item and will need reassess once commissioned, which an
	// authorship hold-off would disable permanently; the label names
	// only the seeding window (§forgejo/decisions/transition-table,
	// s-git/maitred#125).
	LabelForestSeeding = "forest-seeding"
)
