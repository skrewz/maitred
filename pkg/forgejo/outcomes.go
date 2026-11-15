package forgejo

// The outcomes view: the current forests of the organisation, enumerated
// from the outcomes repositories for the dashboard
// (§forgejo/observability/the-outcomes-view).

// ForestIssue is one issue of a forest, slimmed to the fields the view
// shows: number, title, state, and HTML URL
// (§forgejo/observability/the-outcomes-view).
type ForestIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// Forest is one current forest: the outcomes repository it grew in, the
// open ideation root, and the outcome tracker the root blocks — or the
// honest absence of one, spelled as a nil tracker so the field is always
// present and a forest without its gate stays visible rather than being
// dropped (§forgejo/observability/the-outcomes-view).
type Forest struct {
	// Repository is the full name of the outcomes repository the root
	// was found in.
	Repository string `json:"repository"`
	// Root is the open ideation issue the forest grew from.
	Root ForestIssue `json:"root"`
	// Tracker is the outcome issue the root blocks, or nil when the
	// root blocks no outcome issue — the absence stated, not papered
	// over (§forgejo/observability/the-outcomes-view).
	Tracker *ForestIssue `json:"tracker"`
}

// forestIssue slims an issue to its row in the view.
func forestIssue(i Issue) ForestIssue {
	return ForestIssue{
		Number:  i.Number,
		Title:   i.Title,
		State:   i.State,
		HTMLURL: i.HTMLURL,
	}
}

// Forests enumerates the current forests
// (§forgejo/observability/the-outcomes-view): it walks the
// repositories the scope cache classifies as outcomes repositories
// (§forgejo/webhook/repository-roles), lists each one's open issues,
// and pairs every ideation issue with the issue it blocks that carries
// the outcome label (Client.IssueBlocks, the same read the cascade
// follows, §forgejo/client/operations). A root whose tracker is missing
// is listed with the absence stated; an outcome issue no open root
// blocks is not a forest and is not listed. Pull requests carry no
// forest. The list is empty — never nil — when no repository holds the
// role. A non-nil error means a read failed — the organisation listing,
// an issue listing, or a blocks read — and the partial result is
// dropped: a failed read must not read as an empty set of forests, the
// same fail-closed discipline as the scope test
// (§forgejo/webhook/the-scope-cache).
func (e *Engine) Forests() ([]Forest, error) {
	repos, err := e.outcomesRepositories()
	if err != nil {
		return nil, err
	}
	forests := []Forest{}
	for _, full := range repos {
		owner, name := splitRepo(full)
		issues, err := e.api.ListOpenIssues(owner, name)
		if err != nil {
			return nil, err
		}
		for j := range issues {
			issue := &issues[j]
			if issue.IsPull || !hasLabel(issue, LabelIdeation) {
				continue
			}
			forest := Forest{Repository: full, Root: forestIssue(*issue)}
			blocked, err := e.api.IssueBlocks(owner, name, issue.Number)
			if err != nil {
				return nil, err
			}
			for k := range blocked {
				b := &blocked[k]
				if b.IsPull || !hasLabel(b, LabelOutcome) {
					continue
				}
				tracker := forestIssue(*b)
				forest.Tracker = &tracker
				break
			}
			forests = append(forests, forest)
		}
	}
	return forests, nil
}
