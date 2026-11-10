package forgejo

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The vip label decides the tier a task is scheduled at: an object
// carrying it — or a forest whose tracker carries it — is dispatched at
// `teacher` instead of the queue's default (§forgejo/webhook/vip-priority).

// vipIssue returns a fixture issue carrying the vip label.
func vipIssue(number int, state string) *Issue {
	issue := testIssue(number, state)
	issue.Labels = append(issue.Labels, LabelVIP)
	return issue
}

// vipTrackerIssue returns a fixture forest tracker — the `outcome`-labelled
// issue whose task scheduling reads for a vip label
// (§forgejo/webhook/vip-priority).
func vipTrackerIssue(number int, state string, carries bool) Issue {
	issue := testIssue(number, state)
	issue.Labels = []string{LabelOutcome}
	if carries {
		issue.Labels = append(issue.Labels, LabelVIP)
	}
	return *issue
}

// vipRootIssue returns a fixture `ideation` root — the gate that makes an
// `outcome` issue the tracker of the forest a member belongs to
// (§forgejo/webhook/vip-priority).
func vipRootIssue(number int, state string) Issue {
	issue := testIssue(number, state)
	issue.Labels = []string{LabelIdeation}
	return *issue
}

// vipPR returns a fixture pull request carrying the vip label.
func vipPR(number int, state string, mergeable bool) *PullRequest {
	pr := testPR(number, state, mergeable)
	pr.Labels = append(pr.Labels, LabelVIP)
	return pr
}

// TestDispatch_VIPPriority: the tier a forgejoeng dispatch carries is the
// `vip` label's doing alone — the object's own label, the label on the issue
// a pull request delivers, and the label on the tracker of the forest an
// issue belongs to each lift it to `teacher`, while an unlabelled object (or
// one whose forest cannot be read) leaves it unset for the queue's default
// (§forgejo/webhook/vip-priority).
func TestDispatch_VIPPriority(t *testing.T) {
	cases := []struct {
		name   string
		ev     Event
		st     State
		prompt bool
		setup  func(*fakeAPI)
		want   string
		reads  map[string]int
		errSet func(*fakeAPI)
	}{
		{
			name: "the issue carries vip",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: vipIssue(7, "open")},
			want: PriorityTeacher,
		},
		{
			name: "the issue carries nothing",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: testIssue(7, "open")},
			want: "",
			reads: map[string]int{
				// no label to follow: the walk pays no hop at all
				"IssueBlocks:r/7":       0,
				"IssueDependencies:r/7": 0,
				"GetIssue:r/5":          0,
			},
		},
		{
			// The forest's tracker carries vip and gates the member
			// beside the root the whole forest shares
			// (§forgejo/webhook/vip-priority).
			name: "the vip tracker gates the member beside the root",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st: State{Issue: testIssue(7, "open"), Blockers: []Issue{
				vipTrackerIssue(5, "closed", true), vipRootIssue(2, "closed"),
			}},
			setup: func(api *fakeAPI) {
				api.deps[5] = []Issue{vipRootIssue(2, "closed")}
			},
			want: PriorityTeacher,
		},
		{
			name: "the issue is itself the vip tracker",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 5, Sender: "alice"},
			st: State{Issue: &Issue{
				Number: 5, Title: "tracker 5", State: "open",
				Labels: []string{LabelOutcome, LabelVIP}, UpdatedAt: testUpdatedAt,
				HTMLURL: "https://forge.example.com/o/r/issues/5", Repository: "o/r",
			}},
			want: PriorityTeacher,
		},
		{
			// The tracker gates the member: the label is read from the
			// member's own blocker rows, so the walk pays one hop — the
			// tracker's gate — and nothing else.
			name: "blocked by the vip tracker",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "closed", true)}},
			setup: func(api *fakeAPI) {
				api.deps[5] = []Issue{vipRootIssue(2, "closed")}
			},
			want: PriorityTeacher,
			reads: map[string]int{
				"IssueBlocks:r/5": 0, // the member's rows settle it
				"GetIssue:r/5":    0,
			},
		},
		{
			// An outcome issue blocking the member is not a forest tracker
			// unless the shared ideation root gates it: membership is read,
			// not inferred from the label alone.
			name: "the blocked outcome issue is not a gated tracker",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "open", true)}},
			setup: func(api *fakeAPI) {
				api.deps[5] = []Issue{*testIssue(4, "closed")}
			},
			want: "",
		},
		{
			// The forest membership holds, but no carrier wears the label:
			// the queue's own default applies.
			name: "the forest tracker carries no vip",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "open", false)}},
			setup: func(api *fakeAPI) {
				api.deps[5] = []Issue{vipRootIssue(2, "closed")}
			},
			want: "",
			reads: map[string]int{
				"IssueDependencies:r/5": 0, // no label: the gate is never read
			},
		},
		{
			name: "the pull request carries vip",
			ev:   Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:   State{PR: vipPR(9, "open", true)},
			want: PriorityTeacher,
		},
		{
			name: "nothing is scheduled at vip",
			ev:   Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:   State{PR: testPR(9, "open", true)},
			want: "",
		},
		{
			// The issue the pull request delivers carries vip — read from
			// the connected-issue rows the merge gate already fetched.
			name: "the delivered issue carries vip",
			ev:   Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st: State{PR: testPR(9, "open", true), ConnectedIssues: []ConnectedIssue{
				{Issue: *vipIssue(7, "open")},
			}},
			want: PriorityTeacher,
		},
		// The delivered issue is gated by the forest's vip tracker.
		{
			name: "the delivered issue's tracker carries vip",
			ev:   Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st: State{PR: testPR(9, "open", true), ConnectedIssues: []ConnectedIssue{
				{Issue: *testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "closed", true)}},
			}},
			setup: func(api *fakeAPI) {
				api.deps[5] = []Issue{vipRootIssue(2, "closed")}
			},
			want:  PriorityTeacher,
			reads: map[string]int{"IssueDependencies:r/5": 1},
		},
		{
			// A tracker with no vip costs one membership read, not a walk.
			name: "the delivered issue is plain",
			ev:   Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st: State{PR: testPR(9, "open", true), ConnectedIssues: []ConnectedIssue{
				{Issue: *testIssue(7, "open"), Blockers: []Issue{*testIssue(3, "closed")}},
			}},
			want:  "",
			reads: map[string]int{"IssueDependencies:r/5": 0},
		},
		{
			// A state carrying no object — the sweep's housekeeping row —
			// has nothing to judge and leaves the priority unset.
			name:   "the state carries no object",
			prompt: true,
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{},
			want:   "",
		},
		{
			// A failed graph read is not a vip answer: the dispatch stands
			// and the queue's default tier applies.
			name: "the tracker's gate read fails",
			ev:   Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:   State{Issue: testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "open", true)}},
			errSet: func(api *fakeAPI) {
				api.issueDepsErr = errors.New("dependencies unavailable")
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, _ := newTestEngine(t)
			if tc.prompt {
				eng.cfg.Actions[string(ActionWrapUp)] = PromptConfig{
					Prompt: "wrap up {{.Repo}}", Persona: "sweeper", Timeout: time.Hour,
				}
			}
			if tc.setup != nil {
				tc.setup(api)
			}
			if tc.errSet != nil {
				tc.errSet(api)
			}
			action := ActionImplement
			if tc.ev.Kind == KindPR {
				action = ActionMergeOrWait
			}
			if tc.prompt {
				action = ActionWrapUp
			}
			if err := eng.dispatch(tc.ev, tc.st, action); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1", len(tasks))
			}
			if tasks[0].Priority != tc.want {
				t.Errorf("priority = %q, want %q", tasks[0].Priority, tc.want)
			}
			for op, want := range tc.reads {
				if got := api.callCount(op); got != want {
					t.Errorf("%s calls = %d, want %d", op, got, want)
				}
			}
		})
	}
}

// TestDispatch_VIPReadFresh: the label is judged at the moment of
// scheduling and never cached, so removing vip takes effect on the next
// scheduling and re-applying it takes effect again
// (§forgejo/webhook/vip-priority).
func TestDispatch_VIPReadFresh(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	issue := testIssue(7, "open")
	api.issues[7] = issue

	if err := eng.dispatch(Event{Type: EventIssueEdited, Repo: "o/r", Kind: KindIssue, Number: 7}, State{Issue: issue}, ActionReassess); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	vip := testIssue(7, "open")
	vip.Labels = append(vip.Labels, LabelVIP)
	vip.UpdatedAt = issue.UpdatedAt.Add(time.Minute)
	api.issues[7] = vip
	if err := eng.dispatch(Event{Type: EventIssueLabelsChanged, Repo: "o/r", Kind: KindIssue, Number: 7}, State{Issue: vip}, ActionReassess); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	plain := testIssue(7, "open")
	plain.Labels = []string{LabelAgenticAutoMerge}
	plain.UpdatedAt = vip.UpdatedAt.Add(time.Minute)
	api.issues[7] = plain
	if err := eng.dispatch(Event{Type: EventIssueLabelsChanged, Repo: "o/r", Kind: KindIssue, Number: 7}, State{Issue: plain}, ActionReassess); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	tasks := q.all()
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(tasks))
	}
	want := []string{"", PriorityTeacher, ""}
	for i, w := range want {
		if tasks[i].Priority != w {
			t.Errorf("task %d priority = %q, want %q", i, tasks[i].Priority, w)
		}
	}
}

// TestTaskPriority_BlockerCarriesNoLabel: the label is judged on the
// carriers of the forest — the member and its tracker — so an unlabelled
// blocker leaves the dispatch at the queue's default tier.
func TestTaskPriority_BlockerCarriesNoLabel(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.deps[5] = []Issue{vipRootIssue(2, "closed")}
	if got := eng.taskPriority(State{
		Issue: testIssue(7, "open"), Blockers: []Issue{vipTrackerIssue(5, "closed", false)},
	}); got != "" {
		t.Errorf("priority = %q, want unset", got)
	}
}

// TestTaskPriority_CrossRepoTracker: the tracker may live in another
// repository — the membership read is made in the tracker's own
// (§forgejo/webhook/vip-priority, §forgejo/client/operations).
func TestTaskPriority_CrossRepoTracker(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	tracker := vipTrackerIssue(5, "open", true)
	tracker.Repository = "o/other"
	api.deps[5] = []Issue{vipRootIssue(2, "closed")}
	if got := eng.taskPriority(State{
		Issue: testIssue(7, "open"), Blockers: []Issue{tracker},
	}); got != PriorityTeacher {
		t.Errorf("priority = %q, want %q", got, PriorityTeacher)
	}
	if n := api.callCount("IssueDependencies:other/5"); n != 1 {
		t.Errorf("IssueDependencies on the tracker = %d, want 1 (read in its own repo)", n)
	}
}

// TestTaskPriority_CaseInsensitive: the label is matched as every other
// reserved name is, so a carrier labelled "VIP" is not silently
// downgraded (§forgejo/webhook/vip-priority).
func TestTaskPriority_CaseInsensitive(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	pr := testPR(9, "open", true)
	pr.Labels = []string{"VIP"}
	if got := eng.taskPriority(State{PR: pr}); got != PriorityTeacher {
		t.Errorf("priority = %q, want %q", got, PriorityTeacher)
	}
}

// TestTaskPriority_TrackerNamesNoRepository: a blocker row that names no
// repository names no forest — the walk cannot read a gate it cannot locate,
// so the dispatch keeps the queue's default tier rather than guessing
// (§forgejo/webhook/vip-priority, §forgejo/client/operations).
func TestTaskPriority_TrackerNamesNoRepository(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	tracker := vipTrackerIssue(5, "closed", true)
	tracker.Repository = ""
	if got := eng.taskPriority(State{Issue: testIssue(7, "open"), Blockers: []Issue{tracker}}); got != "" {
		t.Errorf("priority = %q, want unset", got)
	}
	if n := api.callCount("IssueDependencies"); n != 0 {
		t.Errorf("IssueDependencies calls = %d, want 0 (nowhere to read the gate)", n)
	}
}

// TestTaskPriority_NoObject: with neither an issue nor a pull request in the
// state there is nothing to judge, and a dispatch of such a state — the
// housekeeping row — carries no tier.
func TestTaskPriority_NoObject(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	if got := eng.taskPriority(State{}); got != "" {
		t.Errorf("priority = %q, want unset", got)
	}
}

// TestDispatch_VIPTierRefusedRetriesAtDefault: a queue that refuses the
// asked tier must not turn the vip label into a permanent hold-off — the
// dispatch is retried once at the queue's default and succeeds, where an
// unset priority was never refused in the first place
// (§forgejo/webhook/vip-priority).
func TestDispatch_VIPTierRefusedRetriesAtDefault(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	q.priorityErr = errors.New("queue refused the tier")
	issue := vipIssue(7, "open")
	api.issues[7] = issue

	if err := eng.dispatch(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"}, State{Issue: issue}, ActionImplement); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Priority != "" {
		t.Errorf("priority = %q, want unset on the retry", tasks[0].Priority)
	}

	// An unlabelled dispatch asks for nothing: the refusing queue never
	// sees the field and nothing changes on its path.
	plain := testIssue(8, "open")
	api.issues[8] = plain
	if err := eng.dispatch(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 8, Sender: "alice"}, State{Issue: plain}, ActionImplement); err != nil {
		t.Fatalf("dispatch plain: %v", err)
	}
	tasks = q.all()
	if len(tasks) != 2 || tasks[1].Priority != "" {
		t.Errorf("tasks = %d with last priority %q, want 2 with unset", len(tasks), tasks[len(tasks)-1].Priority)
	}
}

// TestPriorityVocabulary: the tier named by the policy is one the queue
// recognises, and the label's name is the org-wide one.
func TestPriorityVocabulary(t *testing.T) {
	if PriorityTeacher != "teacher" {
		t.Errorf("PriorityTeacher = %q, want the queue's teacher tier", PriorityTeacher)
	}
	if LabelVIP != "vip" {
		t.Errorf("LabelVIP = %q, want the org-wide vip label", LabelVIP)
	}
	if strings.Contains(PriorityTeacher, " ") {
		t.Error("the tier name must be a single token")
	}
}
