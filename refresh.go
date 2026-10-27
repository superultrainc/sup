package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Refresh runs in two steps. Discovery lists every open PR with cheap fields;
// GitHub search slows down 2-4x once it also computes diff stats or review
// state (~1.3s vs 3-6s for 114 PRs). New PRs show up from discovery right
// away as partial rows; full rows are then fetched in aliased batches, only
// for PRs that are new, partial, or whose updatedAt changed.

// prStub is what discovery returns: enough to show a row and tell whether a
// cached one is current.
type prStub struct {
	Number      int       `json:"number"`
	UpdatedAt   time.Time `json:"updatedAt"`
	HeadRefOid  string    `json:"headRefOid"`
	Title       string    `json:"title"`
	HeadRefName string    `json:"headRefName"`
	IsDraft     bool      `json:"isDraft"`
	Author      ghActor   `json:"author"`
	Repository  struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func (s prStub) asPR() PR {
	var pr PR
	pr.Number, pr.UpdatedAt, pr.HeadRefOid = s.Number, s.UpdatedAt, s.HeadRefOid
	pr.Title, pr.HeadRefName, pr.IsDraft, pr.Author.Login = s.Title, s.HeadRefName, s.IsDraft, s.Author.Login
	pr.Repository.Name, pr.Repository.Owner.Login = s.Repository.Name, s.Repository.Owner.Login
	return pr
}

func searchShards() []string {
	if mineMode {
		return []string{"involves:@me is:pr is:open"}
	}
	var s []string
	for _, o := range orgs {
		s = append(s, "org:"+o+" is:pr is:open")
	}
	return s
}

type discoveredMsg struct {
	refreshID int
	stubs     []prStub
	err       error
}

type searchPage struct {
	stubs      []prStub
	issueCount int
	endCursor  string
	hasNext    bool
}

const searchPageSize = 100 // GitHub's max

func fetchSearchPage(query, after string) (searchPage, error) {
	afterArg := "null"
	if after != "" {
		afterArg = fmt.Sprintf("%q", after)
	}
	out, err := graphqlPOST(fmt.Sprintf(`{ search(query: %q, type: ISSUE, first: %d, after: %s) {
		issueCount pageInfo { endCursor hasNextPage }
		nodes { ... on PullRequest { number updatedAt headRefOid title headRefName isDraft author { login } repository { name owner { login } } } } } }`,
		query, searchPageSize, afterArg))
	if err != nil {
		return searchPage{}, fmt.Errorf("failed to fetch PRs: %w", err)
	}
	var resp struct {
		Data struct {
			Search struct {
				IssueCount int `json:"issueCount"`
				PageInfo   struct {
					EndCursor   string `json:"endCursor"`
					HasNextPage bool   `json:"hasNextPage"`
				} `json:"pageInfo"`
				Nodes []prStub `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return searchPage{}, fmt.Errorf("failed to parse PRs: %w", err)
	}
	s := resp.Data.Search
	return searchPage{s.Nodes, s.IssueCount, s.PageInfo.EndCursor, s.PageInfo.HasNextPage}, nil
}

// discoverCmd lists a query's open PRs. Pages are sequential, so it asks for
// newest-first and oldest-first in parallel: up to 200 PRs in one round trip.
// Bigger sets keep paging the newest-first side.
func discoverCmd(query string, refreshID int) tea.Cmd {
	return func() tea.Msg {
		if demoMode {
			var stubs []prStub
			for _, pr := range mockPRs() {
				var s prStub
				s.Number, s.Repository.Name, s.Repository.Owner.Login = pr.Number, pr.Repository.Name, pr.Repository.Owner.Login
				stubs = append(stubs, s)
			}
			return discoveredMsg{refreshID: refreshID, stubs: stubs}
		}
		var desc, asc searchPage
		var descErr, ascErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); desc, descErr = fetchSearchPage(query+" sort:created-desc", "") }()
		go func() { defer wg.Done(); asc, ascErr = fetchSearchPage(query+" sort:created-asc", "") }()
		wg.Wait()
		if descErr != nil {
			return discoveredMsg{refreshID: refreshID, err: descErr}
		}

		seen := map[string]bool{}
		var stubs []prStub
		add := func(page []prStub) {
			for _, s := range page {
				if k := prKey(s.asPR()); !seen[k] {
					seen[k] = true
					stubs = append(stubs, s)
				}
			}
		}
		add(desc.stubs)
		if ascErr == nil {
			add(asc.stubs)
		}
		for page := desc; page.hasNext && len(stubs) < desc.issueCount; {
			next, err := fetchSearchPage(query+" sort:created-desc", page.endCursor)
			if err != nil {
				return discoveredMsg{refreshID: refreshID, err: err}
			}
			add(next.stubs)
			page = next
		}
		return discoveredMsg{refreshID: refreshID, stubs: stubs}
	}
}

// startRefresh begins discovery for every search shard.
func (m *model) startRefresh() tea.Cmd {
	m.refreshID++
	m.discovered = map[string]prStub{}
	m.discoverFailed = false
	m.pendingRows = 0
	shards := searchShards()
	if len(shards) == 0 {
		m.refreshing = false
		return nil
	}
	m.pendingDiscover = len(shards)
	cmds := []tea.Cmd{spinnerTick()}
	for _, q := range shards {
		cmds = append(cmds, discoverCmd(q, m.refreshID))
	}
	return tea.Batch(cmds...)
}

// handleDiscovered applies each shard as it lands: new PRs appear at once as
// partial rows and changed ones are queued for full rows. Closed PRs are
// pruned only once every shard is in.
func (m *model) handleDiscovered(msg discoveredMsg) tea.Cmd {
	if msg.refreshID != m.refreshID {
		return nil
	}
	m.pendingDiscover--
	if msg.err != nil {
		if len(m.prs) == 0 {
			m.err = msg.err
		}
		m.discoverFailed = true
	}

	index := make(map[string]int, len(m.prs))
	for i := range m.prs {
		index[prKey(m.prs[i])] = i
	}
	var need []PR
	added := false
	for _, s := range msg.stubs {
		key := prKey(s.asPR())
		m.discovered[key] = s
		i, ok := index[key]
		if !ok {
			pr := s.asPR()
			pr.Partial = true
			index[key] = len(m.prs)
			m.prs = append(m.prs, pr)
			need = append(need, pr)
			added = true
			continue
		}
		pr := &m.prs[i]
		if pr.Partial || !pr.UpdatedAt.Equal(s.UpdatedAt) || pr.HeadRefOid != s.HeadRefOid {
			// Show the new title/branch now; stats and review state follow.
			pr.Title, pr.HeadRefName, pr.IsDraft = s.Title, s.HeadRefName, s.IsDraft
			need = append(need, s.asPR())
		}
	}

	if m.pendingDiscover == 0 && !m.discoverFailed {
		kept := m.prs[:0]
		for _, pr := range m.prs {
			if _, open := m.discovered[prKey(pr)]; open {
				kept = append(kept, pr)
			}
		}
		m.prs = kept
	}
	if added {
		sortPRsByOldestFirst(m.prs)
	}
	m.syncRepoList()
	m.applyFilterKeepingCursor()
	m.visibleCount = len(m.filtered)
	if len(m.prs) > 0 {
		m.loading = false
	}

	// Newest first, so the PRs most likely to have changed land first.
	sort.Slice(need, func(i, j int) bool { return need[i].Number > need[j].Number })
	var cmds []tea.Cmd
	for i := 0; i < len(need); i += prRefreshBatch {
		cmds = append(cmds, fetchPRBatchCmd(need[i:min(i+prRefreshBatch, len(need))], m.refreshID))
	}
	m.pendingRows += len(cmds)
	if m.pendingDiscover == 0 && m.pendingRows == 0 {
		cmds = append(cmds, m.finishRefresh())
	}
	return tea.Batch(cmds...)
}

// handleRowsRefreshed applies a batch of fetched rows, adding new PRs.
func (m *model) handleRowsRefreshed(msg prsRefreshedMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.err == nil {
		cmds = append(cmds, m.applyRefreshedPRs(msg.prs))
	}
	if msg.refreshID != 0 && msg.refreshID == m.refreshID {
		m.loading = false
		if m.pendingRows--; m.pendingRows <= 0 && m.pendingDiscover == 0 {
			cmds = append(cmds, m.finishRefresh())
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) finishRefresh() tea.Cmd {
	m.refreshing, m.loading = false, false
	id := m.refreshID
	// Keep the list current while sup stays open, so it's fresh whenever you look.
	next := tea.Tick(autoRefreshEvery, func(time.Time) tea.Msg { return autoRefreshMsg{refreshID: id} })
	if m.discoverFailed {
		return next // not up to date; just try again later
	}
	m.lastRefresh = time.Now()
	// Fresh updatedAt/head values: re-warm anything that changed.
	return tea.Batch(m.enqueueWarm(), m.markPRsDirty(), next)
}

const (
	autoRefreshEvery  = 2 * time.Minute
	focusRefreshAfter = 30 * time.Second
)

type autoRefreshMsg struct{ refreshID int }

type clockTickMsg struct{}

func clockTick() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return clockTickMsg{} })
}

// freshness is the footer's quiet refresh status.
func (m model) freshness() string {
	switch {
	case m.refreshing:
		return "↻ updating"
	case m.lastRefresh.IsZero():
		return ""
	case time.Since(m.lastRefresh) < time.Minute:
		return "✓ up to date"
	}
	return "updated " + relTime(m.lastRefresh)
}
