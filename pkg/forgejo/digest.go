package forgejo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// digestComponent is one component of a forest's actionable state:
// its key — the first part of each folded fact of that component, so
// the fold groups by it for free — and the phrase the dispatch reason
// uses to name it, so a run firing at an unexpected time can be
// diagnosed (§forgejo/decisions/actionable-state-digest,
// s-git/maitred#209). The slice is the component set, in reporting
// order; key and phrase travel together so no component can gain a key
// without a name.
type digestComponent struct {
	key    string
	phrase string
}

var digestComponents = []digestComponent{
	{"root", "the root's state, labels or body"},
	{"member", "a member's state or kind/landing labels"},
	{"tracker", "a tracker's state"},
	{"blocker", "a blocker's state"},
	{"comment", "a comment authored outside the engine"},
}

// digestMemberLabels are the kind and landing labels the digest folds
// for a forest member: the labels that change what a decompose re-run
// could act on — a member becoming a human task, a sync grouping, or
// moving between the solo and landing-set merge opt-ins
// (§forgejo/decisions/actionable-state-digest). Other labels a member
// carries are not folded: they change what a work item looks like, not
// what the plan can do with it.
var digestMemberLabels = []string{
	LabelIdeation,
	LabelOutcome,
	LabelSync,
	LabelHumanTask,
	LabelAgenticAutoMerge,
	LabelAgenticSyncMerge,
}

// ForestDigest computes the actionable-state digest of one forest: a
// hex SHA-256 over exactly the facts a decompose re-run could act on,
// so the same state always yields a byte-identical digest and a change
// to any folded fact moves it (§forgejo/decisions/actionable-state-digest).
// The digest is deterministic and order-insensitive — listings are
// folded as sets of facts, never as a transcript — and a forest's last
// observed digest can be stored through the state store's forest slot
// (§forgejo/state/forest-digest), and the digest gates the `decompose`
// dispatch on the digest moving. Alongside the overall digest the
// function reports the fold's sub-digest per component present in the
// fold (digestComponents): the gate stores both, because naming which
// component moved between two observations needs the per-component
// baseline — a changed overall digest alone says only that something
// moved (§forgejo/decisions/actionable-state-digest, s-git/maitred#209).
//
// The forest is named by its ideation root and read through the issues
// the root blocks: isTracker's reading of those rows — the outcome label
// without ideation — names the forest's trackers, the rest its members,
// so the digest and the decision code cannot disagree about what a row
// is. Folded in are the root's state, labels and body; every member's
// state and kind/landing labels; the state of every blocker of every
// member — read through
// IssueDependencies so blockers outside the forest are included,
// because a member becoming implementable when an unrelated issue
// closes is exactly what should be allowed to change the plan; and
// comments on the root, the trackers and the members whose author is
// neither the engine's own account (engineAccount) nor the configured
// decomposer identity (decomposerIdentity,
// §forgejo/config/decomposer-identity): the operator's comment must be
// a trigger, not a side effect. Deliberately excluded, because each
// would re-trigger the run that wrote it, are the trackers' bodies
// (which carry the current-state block the engine rewrites every run),
// the trackers' updated_at, members' bodies, and any comment authored
// by the decomposer identity or the engine's own account. The engine's
// own account is required: unlike the optional decomposer identity, an
// unset one folds the engine's own report comments, so the digest moves
// on every sweep and each run re-triggers the run that wrote it once
// dispatch is gated on it. An empty comment author is never treated as
// an unset identity.
//
// A failed read is an error, not an omission: no digest is reported
// rather than a partial one, and the caller decides what a missing
// digest means. The digest reads no timestamp of a folded issue and
// makes no call beyond the blocks listing, the members' dependencies
// and the comments of the root, trackers and members.
func ForestDigest(api API, root *Issue, engineAccount, decomposerIdentity string) (string, map[string]string, error) {
	f, err := forestFold(api, root, engineAccount, decomposerIdentity)
	if err != nil {
		return "", nil, err
	}
	return f.sum(), f.componentSums(), nil
}

// forestFold reads the forest and folds its actionable facts.
func forestFold(api API, root *Issue, engineAccount, decomposerIdentity string) (*fold, error) {
	if root == nil {
		return nil, fmt.Errorf("forgejo: forest digest: no root given")
	}
	if engineAccount == "" {
		return nil, fmt.Errorf("forgejo: forest digest: no engine account given")
	}
	f := &fold{}
	rootRef := forestRef(root.Repository, root.Number)
	f.fact("root", rootRef, "state", root.State)
	for _, label := range root.Labels {
		f.fact("root", rootRef, "label", label)
	}
	f.fact("root", rootRef, "body", root.Body)
	if err := foldComments(f, api, rootRef, root.Repository, root.Number, engineAccount, decomposerIdentity); err != nil {
		return nil, err
	}

	rootOwner, rootName := splitRepo(root.Repository)
	blocked, err := api.IssueBlocks(rootOwner, rootName, root.Number)
	if err != nil {
		return nil, fmt.Errorf("forgejo: forest digest: issues root %s blocks: %w", rootRef, err)
	}

	// The root's blocks are the forest: isTracker rows are its trackers,
	// the rest its members. A row that lists no repository lives where
	// the root does — the blocks listing is the root's own relation.
	blockers := map[string]string{}
	for _, b := range blocked {
		repo := b.Repository
		if repo == "" {
			repo = root.Repository
		}
		ref := forestRef(repo, b.Number)
		kind := "member"
		tracker := isTracker(&b)
		if tracker {
			kind = "tracker"
		}
		// Every forest row contributes its state: a closed tracker ends
		// the forest and a closed member is done, either of which is
		// plainly actionable.
		f.fact(kind, ref, "state", b.State)
		if !tracker {
			// A member adds its kind/landing labels and the state of every
			// blocker it has, read through IssueDependencies so blockers
			// outside the forest are included. A tracker contributes nothing
			// further: its blockers say nothing about what the plan can do
			// next, and its body — the current-state block the engine
			// rewrites every run — and its updated_at are the facts this
			// digest must never read (§forgejo/decisions/actionable-state-digest).
			for _, label := range digestMemberLabels {
				if hasLabel(&b, label) {
					f.fact(kind, ref, "label", label)
				}
			}
			owner, name := splitRepo(repo)
			deps, err := api.IssueDependencies(owner, name, b.Number)
			if err != nil {
				return nil, fmt.Errorf("forgejo: forest digest: blockers of %s: %w", ref, err)
			}
			for _, dep := range deps {
				depRepo := dep.Repository
				if depRepo == "" {
					depRepo = repo
				}
				// Blockers fold by key alone: the same issue blocking
				// two members is one fact, and the first listing to name
				// it states its state — one snapshot cannot state two
				// states for one issue.
				key := forestRef(depRepo, dep.Number)
				if _, ok := blockers[key]; !ok {
					blockers[key] = dep.State
				}
			}
		}
		if err := foldComments(f, api, ref, repo, b.Number, engineAccount, decomposerIdentity); err != nil {
			return nil, err
		}
	}
	for ref, state := range blockers {
		f.fact("blocker", ref, "state", state)
	}
	return f, nil
}

// foldComments folds the comments on one forest issue that are not the
// engine's own side effect: those authored by neither the engine's own
// account nor the configured decomposer identity
// (§forgejo/decisions/actionable-state-digest). It is a self-trigger
// guard, not an authorisation test — a comment surviving it says a fact
// moved, not that its sender may commission work. An unset decomposer
// identity excludes nothing; the engine's own account is required, so it
// is never blank here. An empty comment author — the shape of a response
// without a user object — is never treated as an unset identity, so only
// the identity's own login is excluded.
func foldComments(f *fold, api API, ref, repo string, number int, engineAccount, decomposerIdentity string) error {
	owner, name := splitRepo(repo)
	comments, err := api.ListIssueComments(owner, name, number)
	if err != nil {
		return fmt.Errorf("forgejo: forest digest: comments on %s: %w", ref, err)
	}
	for _, c := range comments {
		if c.Author != "" && (c.Author == engineAccount || c.Author == decomposerIdentity) {
			continue
		}
		f.fact("comment", ref, "author", c.Author, "body", c.Body)
	}
	return nil
}

// forestRef is the digest key of an issue: its repository and number.
func forestRef(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

// fold collects the digest's facts and hashes them. Each fact is a
// length-prefixed concatenation of its parts, so no fact's text can be
// mistaken for another's structure; folding sorts the facts before
// hashing, which is what makes the digest order-insensitive
// (§forgejo/decisions/actionable-state-digest). Each fact also records
// the component it folds for — its first part — so the fold can be
// summed per component without a second read.
type fold struct {
	facts []foldedFact
}

// foldedFact is one fact: its component and its encoded text.
type foldedFact struct {
	component string
	encoded   string
}

// fact adds one folded fact, e.g. ("member", "o/r#7", "state", "open");
// the first part names the component it folds for.
func (f *fold) fact(parts ...string) {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
		b.WriteByte('|')
	}
	f.facts = append(f.facts, foldedFact{component: parts[0], encoded: b.String()})
}

// sum is the hex SHA-256 over the sorted facts.
func (f *fold) sum() string {
	return hashFacts(f.facts)
}

// componentSums is the same hash taken component by component: only the
// components with facts stand in the result, so a component appearing or
// disappearing shows as a sub-digest appearing or disappearing
// (§forgejo/decisions/actionable-state-digest).
func (f *fold) componentSums() map[string]string {
	parts := make(map[string]string, len(digestComponents))
	for _, component := range digestComponents {
		var facts []foldedFact
		for _, fact := range f.facts {
			if fact.component == component.key {
				facts = append(facts, fact)
			}
		}
		if len(facts) == 0 {
			continue
		}
		parts[component.key] = hashFacts(facts)
	}
	return parts
}

// hashFacts is the fold's hash loop, shared by the overall sum and the
// per-component sub-digests: the hex SHA-256 over the facts' encoded
// text in sorted order — the sort is what makes every one of the
// digest's readings order-insensitive.
func hashFacts(facts []foldedFact) string {
	encoded := make([]string, 0, len(facts))
	for _, fact := range facts {
		encoded = append(encoded, fact.encoded)
	}
	sort.Strings(encoded)
	h := sha256.New()
	for _, fact := range encoded {
		fmt.Fprintln(h, fact)
	}
	return hex.EncodeToString(h.Sum(nil))
}
