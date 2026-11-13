package forgejo

import (
	"errors"
	"strings"
	"testing"
)

// Fixtures for the actionable-state digest tests
// (§forgejo/decisions/actionable-state-digest).
//
// The forest these tests fold: root #1 (ideation, built by digestRoot)
// blocks tracker #2 (outcome) and members #3 and #5; member #3 is
// blocked by in-forest blocker #4 and by cross-repo blocker #10; member
// #5 is blocked by #3. Comments live on the root, the tracker, and
// member #3.

const (
	digestEngine     = "engine-bot"
	digestDecomposer = "decomposer"
)

func digestIssue(repo string, number int, state string, labels ...string) Issue {
	return Issue{
		Number:     number,
		State:      state,
		Labels:     labels,
		UpdatedAt:  testTime,
		Repository: repo,
	}
}

// digestForest is the unchanged forest: digests of this fixture must
// agree wherever it is used, and each mutation test changes one fact
// from it.
func digestForest() *fakeAPI {
	f := newFakeAPI()
	tracker := digestIssue("o/r", 2, "open", LabelOutcome)
	tracker.Body = "current-state block, rewritten every run"
	member := digestIssue("o/r", 3, "open", LabelAgenticAutoMerge)
	member.Body = "member three's plan"
	blocker := digestIssue("o/r", 4, "closed")
	syncMember := digestIssue("o/r", 5, "open", LabelSync)
	outside := digestIssue("o/other", 10, "open")

	f.blocks[1] = []Issue{tracker, member, syncMember}
	f.deps[3] = []Issue{blocker, outside}
	f.deps[5] = []Issue{digestIssue("o/r", 3, "open")}
	f.comments = map[int][]Comment{
		1: {{Author: "operator", Body: "do it properly"}},
		2: {{Author: digestDecomposer, Body: "a clarifying reply"}},
		3: {{Author: digestEngine, Body: "a side-effect report"}},
	}
	return f
}

// digestRoot is the fixture forest's root. Every test that needs the
// root — whole or with one fact mutated — starts here, so a fixture
// change cannot half-land.
func digestRoot() Issue {
	root := digestIssue("o/r", 1, "open", LabelIdeation)
	root.Body = "the root's intent"
	return root
}

func digest(t *testing.T, f *fakeAPI) string {
	t.Helper()
	root := digestRoot()
	return digestOf(t, f, &root)
}

func digestOf(t *testing.T, f *fakeAPI, root *Issue) string {
	t.Helper()
	got, err := ForestDigest(f, root, digestEngine, digestDecomposer)
	if err != nil {
		t.Fatalf("ForestDigest: %v", err)
	}
	if len(got) != 64 || strings.TrimLeft(got, "0123456789abcdef") != "" {
		t.Fatalf("digest = %q, want a 64-hex-character SHA-256", got)
	}
	return got
}

func TestForestDigestDeterministic(t *testing.T) {
	first := digest(t, digestForest())
	second := digest(t, digestForest())
	if first != second {
		t.Errorf("digest moved across identical runs: %s vs %s", first, second)
	}
}

// TestForestDigestExcludedFacts pins the exclusions: every fact the
// engine rewrites each run — the tracker's body and updated_at — and
// every comment authored by the decomposer identity or the engine's own
// account must leave the digest byte-identical
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestExcludedFacts(t *testing.T) {
	base := digest(t, digestForest())
	t.Run("tracker body rewritten", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][0].Body = "current-state block, restated"
		if got := digest(t, f); got != base {
			t.Errorf("tracker body moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("tracker updated_at bumped", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][0].UpdatedAt = testTime2
		if got := digest(t, f); got != base {
			t.Errorf("tracker updated_at moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("decomposer comment added", func(t *testing.T) {
		f := digestForest()
		f.comments[2] = append(f.comments[2], Comment{Author: digestDecomposer, Body: "another reply"})
		f.comments[1] = append(f.comments[1], Comment{Author: digestDecomposer, Body: "a clarifying question"})
		if got := digest(t, f); got != base {
			t.Errorf("decomposer comment moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("engine account comment added", func(t *testing.T) {
		f := digestForest()
		f.comments[3] = append(f.comments[3], Comment{Author: digestEngine, Body: "a report"})
		if got := digest(t, f); got != base {
			t.Errorf("engine-account comment moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("member body edited", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].Body = "member three's plan, restated"
		if got := digest(t, f); got != base {
			t.Errorf("member body moved the digest: %s vs %s", got, base)
		}
	})
}

// TestForestDigestTracksActionableFacts pins the inclusions: each fact a
// decompose re-run could act on must move the digest on its own
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestTracksActionableFacts(t *testing.T) {
	base := digest(t, digestForest())
	t.Run("blocker state flipped in forest", func(t *testing.T) {
		f := digestForest()
		f.deps[3][0].State = "open"
		if got := digest(t, f); got == base {
			t.Error("an in-forest blocker opening did not move the digest")
		}
	})
	t.Run("blocker state flipped outside forest", func(t *testing.T) {
		f := digestForest()
		f.deps[3][1].State = "closed"
		if got := digest(t, f); got == base {
			t.Error("a cross-repo blocker closing did not move the digest")
		}
	})
	t.Run("root state flipped", func(t *testing.T) {
		root := digestRoot()
		root.State = "closed"
		if got := digestOf(t, digestForest(), &root); got == base {
			t.Error("closing the root did not move the digest")
		}
	})
	t.Run("root body edited", func(t *testing.T) {
		root := digestRoot()
		root.Body = "the root's intent, restated"
		if got := digestOf(t, digestForest(), &root); got == base {
			t.Error("editing the root's body did not move the digest")
		}
	})
	t.Run("root label changed", func(t *testing.T) {
		root := digestRoot()
		root.Labels = append(root.Labels, LabelVIP)
		if got := digestOf(t, digestForest(), &root); got == base {
			t.Error("adding a label to the root did not move the digest")
		}
	})
	t.Run("member state flipped", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].State = "closed"
		if got := digest(t, f); got == base {
			t.Error("closing a member did not move the digest")
		}
	})
	t.Run("member landing label moved", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].Labels = []string{LabelAgenticSyncMerge}
		if got := digest(t, f); got == base {
			t.Error("moving a member's landing label did not move the digest")
		}
	})
	t.Run("member kind label gained", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].Labels = append(f.blocks[1][1].Labels, LabelHumanTask)
		if got := digest(t, f); got == base {
			t.Error("a member becoming a human task did not move the digest")
		}
	})
	t.Run("member gained", func(t *testing.T) {
		f := digestForest()
		f.blocks[1] = append(f.blocks[1], digestIssue("o/r", 9, "open"))
		if got := digest(t, f); got == base {
			t.Error("gaining a member did not move the digest")
		}
	})
	t.Run("row labelled outcome and ideation folds as a member", func(t *testing.T) {
		// The digest reads "tracker" through isTracker, the one reading
		// the decision code applies, so a row carrying both labels — a
		// root to the order, decompose winning over wrap-up — keeps its
		// labels and its blockers folded rather than being spared as a
		// tracker (§forgejo/decisions/transition-table).
		withLanding := digestForest()
		withLanding.blocks[1] = append(withLanding.blocks[1],
			digestIssue("o/r", 6, "open", LabelOutcome, LabelIdeation, LabelAgenticAutoMerge))
		moved := digestForest()
		moved.blocks[1] = append(moved.blocks[1],
			digestIssue("o/r", 6, "open", LabelOutcome, LabelIdeation, LabelAgenticSyncMerge))
		if digest(t, withLanding) == digest(t, moved) {
			t.Error("moving the landing label of a row carrying both outcome and ideation did not move the digest")
		}
		if got := moved.callCount("IssueDependencies:r/6"); got != 1 {
			t.Errorf("a row carrying both labels was spared as a tracker: IssueDependencies:r/6 called %d times, want 1", got)
		}
	})
	t.Run("human comment added on root", func(t *testing.T) {
		f := digestForest()
		f.comments[1] = append(f.comments[1], Comment{Author: "operator", Body: "one more thing"})
		if got := digest(t, f); got == base {
			t.Error("a comment on the root did not move the digest")
		}
	})
	t.Run("human comment added on tracker", func(t *testing.T) {
		f := digestForest()
		f.comments[2] = append(f.comments[2], Comment{Author: "operator", Body: "hold this one"})
		if got := digest(t, f); got == base {
			t.Error("a comment on the tracker did not move the digest")
		}
	})
	t.Run("human comment added on member", func(t *testing.T) {
		f := digestForest()
		f.comments[3] = append(f.comments[3], Comment{Author: "operator", Body: "look here instead"})
		if got := digest(t, f); got == base {
			t.Error("a comment on a member did not move the digest")
		}
	})
	t.Run("identical comment added twice differs from once", func(t *testing.T) {
		f := digestForest()
		f.comments[1] = append(f.comments[1], Comment{Author: "operator", Body: "do it properly"})
		if got := digest(t, f); got == base {
			t.Error("a repeated human comment did not move the digest")
		}
	})
}

// TestForestDigestOrderInsensitive pins that the digest folds a set of
// facts, not a transcript: permuting the listings moves nothing
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestOrderInsensitive(t *testing.T) {
	base := digest(t, digestForest())
	t.Run("blocked listing reversed", func(t *testing.T) {
		f := digestForest()
		b := f.blocks[1]
		f.blocks[1] = []Issue{b[2], b[1], b[0]}
		if got := digest(t, f); got != base {
			t.Errorf("listing order moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("member labels reordered", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].Labels = []string{LabelAgenticAutoMerge, LabelVIP, "zzz"}
		got := digest(t, f)
		f2 := digestForest()
		f2.blocks[1][1].Labels = []string{"zzz", LabelVIP, LabelAgenticAutoMerge}
		if want := digest(t, f2); got != want {
			t.Errorf("label order moved the digest: %s vs %s", got, want)
		}
	})
	t.Run("comments reordered", func(t *testing.T) {
		f := digestForest()
		f.comments[1] = []Comment{
			{Author: "operator", Body: "do it properly"},
			{Author: "second", Body: "and again"},
		}
		got := digest(t, f)
		f2 := digestForest()
		f2.comments[1] = []Comment{
			{Author: "second", Body: "and again"},
			{Author: "operator", Body: "do it properly"},
		}
		if want := digest(t, f2); got != want {
			t.Errorf("comment order moved the digest: %s vs %s", got, want)
		}
	})
	t.Run("blocker listing reordered", func(t *testing.T) {
		f := digestForest()
		d := f.deps[3]
		f.deps[3] = []Issue{d[1], d[0]}
		if got := digest(t, f); got != base {
			t.Errorf("blocker order moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("blocker shared by two members folds once", func(t *testing.T) {
		// The digest folds a blocker's state, not which member's dependency
		// listing names it, so blocker #4 appearing in member #5's listing
		// alongside member #3's adds no fact. Folding each dependency as it
		// comes would double the fact, and blocking-edge churn would then
		// move the digest with no state having moved — the false re-trigger
		// this feature exists to remove.
		shared := digestIssue("o/r", 4, "closed")
		namedByBoth := digestForest()
		namedByBoth.deps[5] = append(namedByBoth.deps[5], shared)
		if got := digest(t, namedByBoth); got != base {
			t.Errorf("a blocker named by a second member moved the digest: %s vs %s", got, base)
		}
		namedByOtherOnly := digestForest()
		namedByOtherOnly.deps[3] = []Issue{namedByOtherOnly.deps[3][1]}
		namedByOtherOnly.deps[5] = append(namedByOtherOnly.deps[5], shared)
		if got := digest(t, namedByOtherOnly); got != base {
			t.Errorf("which member names a shared blocker moved the digest: %s vs %s", got, base)
		}
	})
	t.Run("shared blocker's listings reordered", func(t *testing.T) {
		shared := digestIssue("o/r", 4, "closed")
		f := digestForest()
		f.deps[5] = append(f.deps[5], shared)
		d3, d5 := f.deps[3], f.deps[5]
		f.deps[3] = []Issue{d3[1], d3[0]}
		f.deps[5] = []Issue{d5[1], d5[0]}
		if got := digest(t, f); got != base {
			t.Errorf("order of the listings naming a shared blocker moved the digest: %s vs %s", got, base)
		}
	})
}

// TestForestDigestReadFailures pins that a failed read is an error, not
// an omission: the digest reports no value rather than a partial one
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestReadFailures(t *testing.T) {
	root := digestRoot()
	t.Run("no root", func(t *testing.T) {
		if _, err := ForestDigest(digestForest(), nil, digestEngine, digestDecomposer); err == nil {
			t.Fatal("a forest with no root digested without error")
		}
	})
	t.Run("blocks listing fails", func(t *testing.T) {
		f := digestForest()
		f.issueBlocksErr = errors.New("boom")
		if _, err := ForestDigest(f, &root, digestEngine, digestDecomposer); err == nil {
			t.Fatal("a failed blocks read digested without error")
		}
	})
	t.Run("dependencies fail", func(t *testing.T) {
		f := digestForest()
		f.depsErr = errors.New("boom")
		if _, err := ForestDigest(f, &root, digestEngine, digestDecomposer); err == nil {
			t.Fatal("a failed dependencies read digested without error")
		}
	})
	t.Run("comments fail", func(t *testing.T) {
		f := digestForest()
		f.commentsErr = errors.New("boom")
		if _, err := ForestDigest(f, &root, digestEngine, digestDecomposer); err == nil {
			t.Fatal("a failed comments read digested without error")
		}
	})
}

// TestForestDigestIdentities pins the two identities' different
// semantics: the decomposer identity is optional, so with it empty its
// comments fold like any other author's
// (§forgejo/config/decomposer-identity), while the engine's own account
// is required — an unset one would fold the engine's own report comments
// and re-trigger every run once dispatch is gated on the digest
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestIdentities(t *testing.T) {
	root := digestRoot()
	f := digestForest()
	withDecomposer, err := ForestDigest(f, &root, digestEngine, digestDecomposer)
	if err != nil {
		t.Fatalf("ForestDigest: %v", err)
	}
	withoutDecomposer, err := ForestDigest(f, &root, digestEngine, "")
	if err != nil {
		t.Fatalf("ForestDigest: %v", err)
	}
	if withDecomposer == withoutDecomposer {
		t.Error("an unset decomposer identity still excluded the decomposer's comments")
	}
	if _, err := ForestDigest(f, &root, "", digestDecomposer); err == nil {
		t.Error("an unset engine account digested without error")
	}
}

// TestForestDigestReadsOnlyTheNamedReads pins the digest's read surface:
// the forest is folded from the blocks listing, the members'
// dependencies and the comments of root, tracker and members — and
// nothing else, so no excluded fact can slip in through an unlisted read
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestReadsOnlyTheNamedReads(t *testing.T) {
	f := digestForest()
	digest(t, f)
	want := map[string]int{
		"IssueBlocks:r/1":       1,
		"IssueDependencies:r/3": 1,
		"IssueDependencies:r/5": 1,
		"Comments:r/1":          1,
		"Comments:r/2":          1,
		"Comments:r/3":          1,
		"Comments:r/5":          1,
		"IssueDependencies:r/2": 0,
		"IssueDependencies:r/1": 0,
		"GetIssue":              0,
	}
	for op, countWant := range want {
		if got := f.callCount(op); got != countWant {
			t.Errorf("op %s called %d times, want %d (calls: %v)", op, got, countWant, f.calls)
		}
	}
}

// TestForestDigestRowsWithoutRepository pins the two repository
// fallbacks: a blocks row listing none lives where the root does, and a
// blocker listing none lives where the member naming it does, so a blank
// repository cannot move the digest from the explicit spelling
// (§forgejo/decisions/actionable-state-digest).
func TestForestDigestRowsWithoutRepository(t *testing.T) {
	base := digest(t, digestForest())
	t.Run("member listing no repository", func(t *testing.T) {
		f := digestForest()
		f.blocks[1][1].Repository = ""
		if got := digest(t, f); got != base {
			t.Errorf("a member listing no repository digested differently: %s vs %s", got, base)
		}
	})
	t.Run("blocker listing no repository", func(t *testing.T) {
		f := digestForest()
		f.deps[3][0].Repository = ""
		if got := digest(t, f); got != base {
			t.Errorf("a blocker listing no repository digested differently: %s vs %s", got, base)
		}
	})
}
