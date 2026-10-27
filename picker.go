package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// reviewerCandidate is a user login or, for teams, "org/slug" (what gh expects).
type reviewerCandidate struct {
	id        string
	name      string
	team      bool
	suggested bool
}

type reviewerPicker struct {
	loading  bool
	saving   bool
	err      error
	cands    []reviewerCandidate
	query    string
	cursor   int
	selected map[string]bool
	initial  map[string]bool
}

// requestedIDs returns the PR's pending review requests as candidate ids.
func requestedIDs(pr PR, d *prDetail) map[string]bool {
	ids := map[string]bool{}
	if d == nil {
		for _, n := range strings.Fields(getRequestedReviewerNames(pr)) {
			ids[n] = true
		}
		return ids
	}
	for _, rr := range d.ReviewRequests.Nodes {
		switch r := rr.RequestedReviewer; {
		case r.Login != "":
			ids[r.Login] = true
		case r.Slug != "":
			ids[pr.Repository.Owner.Login+"/"+r.Slug] = true
		}
	}
	return ids
}

func newReviewerPicker(pr PR, d *prDetail) *reviewerPicker {
	initial := requestedIDs(pr, d)
	selected := map[string]bool{}
	for id := range initial {
		selected[id] = true
	}
	return &reviewerPicker{loading: true, selected: selected, initial: initial}
}

func (p *reviewerPicker) visible() []reviewerCandidate {
	q := strings.ToLower(p.query)
	if q == "" {
		return p.cands
	}
	var out []reviewerCandidate
	for _, c := range p.cands {
		if strings.Contains(strings.ToLower(c.id), q) || strings.Contains(strings.ToLower(c.name), q) {
			out = append(out, c)
		}
	}
	return out
}

func (p *reviewerPicker) changes() (add, remove []string) {
	for id := range p.selected {
		if !p.initial[id] {
			add = append(add, id)
		}
	}
	for id := range p.initial {
		if !p.selected[id] {
			remove = append(remove, id)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

type reviewerCandidatesMsg struct {
	key   string
	cands []reviewerCandidate
	err   error
}

// fetchReviewerCandidatesCmd loads collaborators + suggestions, and org teams
// in parallel; teams are best-effort since user-owned repos have none.
func fetchReviewerCandidatesCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		key := prKey(pr)
		if demoMode {
			return reviewerCandidatesMsg{key: key, cands: []reviewerCandidate{
				{id: "mike", name: "Mike Chen", suggested: true}, {id: "alex", name: "Alex Rivera"},
				{id: "jordan", name: "Jordan Lee"}, {id: "taylor", name: "Taylor Kim"},
				{id: "acme-corp/backend", name: "Backend", team: true},
			}}
		}
		owner, repo := pr.Repository.Owner.Login, pr.Repository.Name
		var wg sync.WaitGroup
		var users struct {
			Data struct {
				Repository struct {
					AssignableUsers struct {
						Nodes []struct{ Login, Name string } `json:"nodes"`
					} `json:"assignableUsers"`
					PullRequest struct {
						SuggestedReviewers []struct {
							Reviewer struct{ Login, Name string } `json:"reviewer"`
						} `json:"suggestedReviewers"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		var teams struct {
			Data struct {
				Organization *struct {
					Teams struct {
						Nodes []struct{ Slug, Name string } `json:"nodes"`
					} `json:"teams"`
				} `json:"organization"`
			} `json:"data"`
		}
		var usersErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			out, err := graphqlPOST(fmt.Sprintf(`{ repository(owner: "%s", name: "%s") {
				assignableUsers(first: 100) { nodes { login name } }
				pullRequest(number: %d) { suggestedReviewers { reviewer { login name } } }
			} }`, owner, repo, pr.Number))
			if err != nil {
				usersErr = err
				return
			}
			usersErr = json.Unmarshal(out, &users)
		}()
		go func() {
			defer wg.Done()
			if out, err := graphqlPOST(fmt.Sprintf(`{ organization(login: "%s") { teams(first: 100) { nodes { slug name } } } }`, owner)); err == nil {
				json.Unmarshal(out, &teams)
			}
		}()
		wg.Wait()
		if usersErr != nil {
			return reviewerCandidatesMsg{key: key, err: usersErr}
		}

		var cands []reviewerCandidate
		seen := map[string]bool{}
		for _, s := range users.Data.Repository.PullRequest.SuggestedReviewers {
			if r := s.Reviewer; r.Login != "" && !seen[r.Login] {
				seen[r.Login] = true
				cands = append(cands, reviewerCandidate{id: r.Login, name: r.Name, suggested: true})
			}
		}
		for _, u := range users.Data.Repository.AssignableUsers.Nodes {
			if !seen[u.Login] {
				seen[u.Login] = true
				cands = append(cands, reviewerCandidate{id: u.Login, name: u.Name})
			}
		}
		if teams.Data.Organization != nil {
			for _, t := range teams.Data.Organization.Teams.Nodes {
				cands = append(cands, reviewerCandidate{id: owner + "/" + t.Slug, name: t.Name, team: true})
			}
		}
		return reviewerCandidatesMsg{key: key, cands: cands}
	}
}

// orderCandidates puts requested, then suggested, then past reviewers first;
// the order is fixed once loaded so rows don't jump while toggling.
func (p *reviewerPicker) setCandidates(cands []reviewerCandidate, author string, reviewed map[string]string) {
	seen := map[string]bool{}
	var filtered []reviewerCandidate
	for _, c := range cands {
		if c.id != author {
			filtered = append(filtered, c)
			seen[c.id] = true
		}
	}
	// Requested reviewers may not be assignable (e.g. outside collaborators); keep them visible.
	for id := range p.initial {
		if !seen[id] {
			filtered = append(filtered, reviewerCandidate{id: id, team: strings.Contains(id, "/")})
		}
	}
	rank := func(c reviewerCandidate) int {
		switch {
		case p.initial[c.id]:
			return 0
		case c.suggested:
			return 1
		case reviewed[c.id] != "":
			return 2
		case c.team:
			return 4
		}
		return 3
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		ri, rj := rank(filtered[i]), rank(filtered[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(filtered[i].id) < strings.ToLower(filtered[j].id)
	})
	p.cands = filtered
	p.loading = false
}

type reviewersSavedMsg struct {
	pr      PR
	added   []string
	removed []string
	err     error
}

func saveReviewersCmd(pr PR, add, remove []string) tea.Cmd {
	return func() tea.Msg {
		msg := reviewersSavedMsg{pr: pr, added: add, removed: remove}
		if demoMode {
			return msg
		}
		path := fmt.Sprintf("%s/pulls/%d/requested_reviewers", repoPath(pr), pr.Number)
		if len(add) > 0 {
			if _, err := githubREST("POST", path, reviewerBody(add)); err != nil {
				msg.err = err
				return msg
			}
		}
		if len(remove) > 0 {
			if _, err := githubREST("DELETE", path, reviewerBody(remove)); err != nil {
				msg.err = err
			}
		}
		return msg
	}
}

// reviewerBody splits candidate ids into users and team slugs ("org/slug").
func reviewerBody(ids []string) map[string][]string {
	body := map[string][]string{"reviewers": {}, "team_reviewers": {}}
	for _, id := range ids {
		if _, slug, ok := strings.Cut(id, "/"); ok {
			body["team_reviewers"] = append(body["team_reviewers"], slug)
		} else {
			body["reviewers"] = append(body["reviewers"], id)
		}
	}
	return body
}

func (m model) handlePickerInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.detail
	p := v.picker
	if p.saving {
		return m, nil
	}
	vis := p.visible()
	switch msg.String() {
	case "esc":
		v.picker = nil
	case "enter":
		add, remove := p.changes()
		if len(add) == 0 && len(remove) == 0 {
			v.picker = nil
			return m, nil
		}
		p.saving = true
		return m, tea.Batch(saveReviewersCmd(v.pr, add, remove), spinnerTick())
	case "up", "ctrl+p", "ctrl+k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "ctrl+n", "ctrl+j":
		p.cursor = min(p.cursor+1, max(len(vis)-1, 0))
	case " ", "tab":
		if p.cursor < len(vis) {
			id := vis[p.cursor].id
			p.selected[id] = !p.selected[id]
			if !p.selected[id] {
				delete(p.selected, id)
			}
		}
	case "backspace", "ctrl+h":
		if p.query != "" {
			r := []rune(p.query)
			p.query = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes {
			p.query += string(msg.Runes)
			p.cursor = 0
		}
	}
	return m, nil
}

func (m *model) handleReviewersSaved(msg reviewersSavedMsg) tea.Cmd {
	v := m.detail
	if v != nil && v.picker != nil {
		v.picker.saving = false
	}
	if msg.err != nil {
		if v != nil && v.picker != nil {
			v.picker.err = msg.err
		} else {
			m.actionStatus = "Error: " + msg.err.Error()
		}
		return nil
	}
	if v != nil {
		v.picker = nil
	}
	var parts []string
	if len(msg.added) > 0 {
		parts = append(parts, "requested "+strings.Join(msg.added, ", "))
	}
	if len(msg.removed) > 0 {
		parts = append(parts, "removed "+strings.Join(msg.removed, ", "))
	}
	m.actionStatus = "✓ Reviewers: " + strings.Join(parts, "; ")
	cmds := []tea.Cmd{fetchSinglePRCmd(msg.pr)}
	if v != nil && prKey(v.pr) == prKey(msg.pr) {
		e := m.cacheEntry(prKey(msg.pr))
		e.detailAt = e.detailAt.AddDate(-1, 0, 0) // force a refetch
		v.loading = true
		cmds = append(cmds, m.fetchMissing(msg.pr, e)...)
		cmds = append(cmds, spinnerTick())
	}
	return tea.Batch(cmds...)
}

// pickerView renders the picker card to fill height rows.
func (m model) pickerView(height int) []string {
	v := m.detail
	p := v.picker
	th := currentDiffTheme()
	width := m.width - 2
	title := sectionHeadStyle.Render("Request reviewers") + metaStyle.Render(" · type to filter · space toggle · enter save · esc cancel")

	body := []string{filterStyle.Render("› ") + p.query + filterStyle.Render("█"), ""}
	latest, _ := latestReviewStates(v.detail)
	vis := p.visible()
	listH := max(height-len(body)-3, 1)
	switch {
	case p.loading:
		body = append(body, reviewRequestedStyle.Render(spinnerFrames[m.spinnerFrame]+" Loading people..."))
	case p.err != nil && len(p.cands) == 0:
		body = append(body, changesRequestedStyle.Render("Error: "+p.err.Error()))
	case len(vis) == 0:
		body = append(body, metaStyle.Render("No matches."))
	default:
		start := 0
		if p.cursor >= listH {
			start = p.cursor - listH + 1
		}
		for i := start; i < min(start+listH, len(vis)); i++ {
			c := vis[i]
			caret := "  "
			if i == p.cursor {
				caret = caretStyle.Render("» ")
			}
			box := metaStyle.Render("[ ] ")
			if p.selected[c.id] {
				box = approvedStyle.Render("[✓] ")
			}
			idStyle := normalStyle
			if i == p.cursor {
				idStyle = selectedNormalStyle
			}
			line := caret + box + idStyle.Render(pad(c.id, 24)) + metaStyle.Render(pad(truncate(c.name, 22), 24))
			var tags []string
			switch latest[c.id] {
			case "APPROVED":
				tags = append(tags, approvedStyle.Render("✓ approved"))
			case "CHANGES_REQUESTED":
				tags = append(tags, changesRequestedStyle.Render("✗ changes requested"))
			case "COMMENTED":
				tags = append(tags, commentedStyle.Render("● commented"))
			}
			if c.suggested {
				tags = append(tags, reviewRequestedStyle.Render("suggested"))
			}
			if c.team {
				tags = append(tags, pathStyle.Render("team"))
			}
			body = append(body, line+strings.Join(tags, "  "))
		}
	}
	if p.saving {
		body = append(body, "", reviewRequestedStyle.Render(spinnerFrames[m.spinnerFrame]+" Saving..."))
	} else if p.err != nil && len(p.cands) > 0 {
		body = append(body, "", changesRequestedStyle.Render("Error: "+p.err.Error()))
	} else if add, remove := p.changes(); len(add)+len(remove) > 0 {
		summary := ""
		if len(add) > 0 {
			summary += approvedStyle.Render("+ " + strings.Join(add, ", "))
		}
		if len(remove) > 0 {
			if summary != "" {
				summary += "   "
			}
			summary += changesRequestedStyle.Render("− " + strings.Join(remove, ", "))
		}
		body = append(body, "", summary)
	}
	out := textCard(title, th.threadBorder, body, width)
	for i := range out {
		out[i] = " " + out[i]
	}
	return out
}
