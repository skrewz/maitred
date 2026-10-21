package forgejo

// Reserved label names (§forgejo/decisions/actions).
//
// This file is the single home of the engine's reserved label vocabulary:
// one definition per reserved name, referenced wherever the name is
// tested. Reserved names are matched by exact, case-sensitive comparison,
// so a name defined twice is a divergence waiting to happen — add to this
// block instead of declaring the name again.
const (
	// LabelIdeation marks an issue whose release gate has not opened: an
	// ideation issue closes when the human releases its forest, so an open
	// issue carrying it blocks the work started against it. It also marks a
	// root to decompose, and — seen from a tracker whose copy of that edge is
	// closed — a forest released but not yet finished
	// (§forgejo/reconciliation/watermark-exemptions).
	LabelIdeation = "ideation"
	// LabelOutcome marks an issue as a tracker to wrap up once all
	// its blockers are closed
	// (§forgejo/reconciliation/watermark-exemptions).
	LabelOutcome = "outcome"
)
