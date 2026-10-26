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
)

// teardownGate names the ideation blocker ordering its own forest
// deleted — the root a tracker is gated by while that forest is being
// taken apart. Its state is irrelevant: the order stands whether the
// root is open (teardown fires) or closed (teardown does not, and the
// tracker must not read as finished while members are being deleted)
// (§forgejo/decisions/forest-teardown).
func teardownGate(blockers []Issue) *Issue {
	for i := range blockers {
		if hasLabel(&blockers[i], LabelIdeation) && hasLabel(&blockers[i], LabelTeardownForest) {
			return &blockers[i]
		}
	}
	return nil
}
