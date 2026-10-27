package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Refresh runs in two steps. Discovery lists every open PR with light fields
// (number, updatedAt, head); GitHub search is slow when it also resolves
// reviews per PR (~7s vs ~1.5s for 114 PRs). Then full rows are fetched in
// aliased batches, only for PRs that are new or whose updatedAt changed.

// prStub is what discovery needs to tell whether a cached row is current.
type prStub struct {
	Number     int       `json:"number"`
	UpdatedAt  time.Time `json:"updatedAt"`
	HeadRefOid string    `json:"headRefOid"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func (s prStub) asPR() PR {
	var pr PR
	pr.Number, pr.UpdatedAt, pr.HeadRefOid = s.Number, s.UpdatedAt, s.HeadRefOid
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
		nodes { ... on PullRequest { number updatedAt headRefOid repository { name owner { login } } } } } }`,
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

// handleDiscovered collects shard results; once all are in, it drops closed
// PRs and fetches rows only for new or updated ones.
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
	for _, s := range msg.stubs {
		m.discovered[prKey(s.asPR())] = s
	}
	if m.pendingDiscover > 0 {
		return nil
	}
	if m.discoverFailed {
		// A partial list can't tell closed PRs from missing ones; keep what we have.
		m.refreshing, m.loading = false, false
		return nil
	}

	have := make(map[string]PR, len(m.prs))
	kept := m.prs[:0]
	for _, pr := range m.prs {
		if _, open := m.discovered[prKey(pr)]; open {
			kept = append(kept, pr)
			have[prKey(pr)] = pr
		}
	}
	m.prs = kept

	var need []PR
	for key, s := range m.discovered {
		if pr, ok := have[key]; !ok || !pr.UpdatedAt.Equal(s.UpdatedAt) || pr.HeadRefOid != s.HeadRefOid {
			need = append(need, s.asPR())
		}
	}
	// Newest first, so the PRs most likely to have changed land first.
	sort.Slice(need, func(i, j int) bool { return need[i].Number > need[j].Number })

	m.syncRepoList()
	m.applyFilterKeepingCursor()
	m.visibleCount = len(m.filtered)
	if len(need) == 0 {
		return m.finishRefresh()
	}
	cmds := make([]tea.Cmd, 0, len(need)/prRefreshBatch+1)
	for i := 0; i < len(need); i += prRefreshBatch {
		cmds = append(cmds, fetchPRBatchCmd(need[i:min(i+prRefreshBatch, len(need))], m.refreshID))
	}
	m.pendingRows = len(cmds)
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
		if m.pendingRows--; m.pendingRows <= 0 {
			cmds = append(cmds, m.finishRefresh())
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) finishRefresh() tea.Cmd {
	m.refreshing, m.loading = false, false
	// Fresh updatedAt/head values: re-warm anything that changed.
	return tea.Batch(m.enqueueWarm(), m.markPRsDirty())
}
