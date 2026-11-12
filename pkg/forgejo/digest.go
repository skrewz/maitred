package forgejo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

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
// folded as sets of facts, never as a transcript — and it is computed,
// not yet consulted: persisting a forest's previous digest and gating
// dispatch on the digest moving are separate work.
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
// by the decomposer identity or the engine's own account. An empty
// identity excludes nothing, matching the optional-identity convention;
// an empty comment author is never treated as an empty identity.
//
// A failed read is an error, not an omission: no digest is reported
// rather than a partial one, and the caller decides what a missing
// digest means. The digest reads no timestamp of a folded issue and
// makes no call beyond the blocks listing, the members' dependencies
// and the comments of the root, trackers and members.
func ForestDigest(api API, root *Issue, engineAccount, decomposerIdentity string) (string, error) {
	if root == nil {
		return "", fmt.Errorf("forgejo: forest digest: no root given")
	}
	f := &fold{}
	rootRef := forestRef(root.Repository, root.Number)
	f.fact("root", rootRef, "state", root.State)
	for _, label := range root.Labels {
		f.fact("root", rootRef, "label", label)
	}
	f.fact("root", rootRef, "body", root.Body)
	if err := foldComments(f, api, rootRef, root.Repository, root.Number, engineAccount, decomposerIdentity); err != nil {
		return "", err
	}

	rootOwner, rootName := splitRepo(root.Repository)
	blocked, err := api.IssueBlocks(rootOwner, rootName, root.Number)
	if err != nil {
		return "", fmt.Errorf("forgejo: forest digest: issues root %s blocks: %w", rootRef, err)
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
				return "", fmt.Errorf("forgejo: forest digest: blockers of %s: %w", ref, err)
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
			return "", err
		}
	}
	for ref, state := range blockers {
		f.fact("blocker", ref, "state", state)
	}
	return f.sum(), nil
}

// foldComments folds the comments on one forest issue that count as
// triggers: those authored by neither the engine's own account nor the
// configured decomposer identity (§forgejo/decisions/actionable-state-
// digest). An unset identity excludes nothing; an empty comment author
// — the shape of a response without a user object — is never treated
// as an unset identity, so only the identity's own login is excluded.
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
// (§forgejo/decisions/actionable-state-digest).
type fold struct {
	facts []string
}

// fact adds one folded fact, e.g. ("member", "o/r#7", "state", "open").
func (f *fold) fact(parts ...string) {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
		b.WriteByte('|')
	}
	f.facts = append(f.facts, b.String())
}

// sum is the hex SHA-256 over the sorted facts.
func (f *fold) sum() string {
	sort.Strings(f.facts)
	h := sha256.New()
	for _, fact := range f.facts {
		fmt.Fprintln(h, fact)
	}
	return hex.EncodeToString(h.Sum(nil))
}
