package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	orgs        []string // Auto-detected or from SUP_ORG
	mineMode    bool     // Show PRs involving current user
	demoMode    bool     // Show mock data for screenshots
	currentUser string   // Authenticated GitHub username
	ghToken     string   // Cached `gh auth token` for direct HTTP calls

	httpClient = &http.Client{Timeout: 30 * time.Second, Transport: newTransport()}
)

// newTransport starts from the default transport so HTTP/2 stays on: parallel
// GitHub requests then share one TLS connection instead of a handshake each.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 20
	t.MaxIdleConnsPerHost = 20
	t.IdleConnTimeout = 90 * time.Second
	return t
}

func tokenCachePath() string { return filepath.Join(cacheDir(), "token") }

func loadGHToken() error {
	if data, err := os.ReadFile(tokenCachePath()); err == nil {
		if t := strings.TrimSpace(string(data)); t != "" {
			ghToken = t
			return nil
		}
	}
	return refreshGHToken()
}

func refreshGHToken() error {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return err
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return fmt.Errorf("empty token from gh auth token")
	}
	ghToken = t
	_ = os.WriteFile(tokenCachePath(), []byte(t), 0600)
	return nil
}

func invalidateGHToken() {
	_ = os.Remove(tokenCachePath())
	ghToken = ""
}

func graphqlPOST(query string) ([]byte, error) {
	data, status, err := graphqlPOSTOnce(query)
	if err != nil {
		return nil, err
	}
	// Token may have rotated since we cached it — refresh once and retry.
	if status == 401 {
		invalidateGHToken()
		if err := refreshGHToken(); err != nil {
			return nil, fmt.Errorf("auth refresh failed: %w", err)
		}
		data, status, err = graphqlPOSTOnce(query)
		if err != nil {
			return nil, err
		}
	}
	if status >= 400 {
		return nil, fmt.Errorf("graphql %d: %s", status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func graphqlPOSTOnce(query string) ([]byte, int, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+ghToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// Output file for shell integration (shell wrapper reads this to cd)
const selectionFile = "/tmp/sup-selection"

// Common locations where repos might be cloned
var defaultDevDirs = []string{
	"Development",
	"dev",
	"projects",
	"code",
	"src",
	"repos",
	"github",
	"git",
	"",
}

func cacheDir() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		dir = os.Getenv("HOME") + "/.cache"
	}
	dir = filepath.Join(dir, "sup")
	os.MkdirAll(dir, 0755)
	return dir
}

type metaCache struct {
	Orgs        []string `json:"orgs"`
	CurrentUser string   `json:"currentUser"`
}

func getMetaCachePath() string { return filepath.Join(cacheDir(), "meta.json") }

func loadCachedMeta() (metaCache, bool) {
	data, err := os.ReadFile(getMetaCachePath())
	if err != nil {
		return metaCache{}, false
	}
	var m metaCache
	if err := json.Unmarshal(data, &m); err != nil {
		return metaCache{}, false
	}
	return m, true
}

func saveCachedMeta(m metaCache) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	os.WriteFile(getMetaCachePath(), data, 0644)
}

func getCacheFilePath() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		cacheDir = os.Getenv("HOME") + "/.cache"
	}
	cacheDir = filepath.Join(cacheDir, "sup")
	os.MkdirAll(cacheDir, 0755)
	return filepath.Join(cacheDir, "prs.json")
}

func loadCachedPRs() []PR {
	data, err := os.ReadFile(getCacheFilePath())
	if err != nil {
		return nil
	}
	var prs []PR
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil
	}
	return prs
}

// savePRsToCache writes atomically so quitting mid-write can't truncate the cache.
func savePRsToCache(prs []PR) {
	data, err := json.Marshal(prs)
	if err != nil {
		return
	}
	path := getCacheFilePath()
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		os.Rename(tmp, path)
	}
}

type savePRsMsg struct{}

// markPRsDirty coalesces cache writes: a refresh delivers ~one response per PR,
// so the list is saved once the burst settles instead of per response.
func (m *model) markPRsDirty() tea.Cmd {
	if demoMode {
		return nil
	}
	m.prsDirty = true
	if m.prsSaveScheduled {
		return nil
	}
	m.prsSaveScheduled = true
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return savePRsMsg{} })
}

func samePR(a, b PR) bool {
	return a.Number == b.Number && a.Repository.Name == b.Repository.Name && a.Repository.Owner.Login == b.Repository.Owner.Login
}

type PR struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	HeadRefName string    `json:"headRefName"`
	HeadRefOid  string    `json:"headRefOid"`
	UpdatedAt   time.Time `json:"updatedAt"`
	IsDraft     bool      `json:"isDraft"`
	Additions   int       `json:"additions"`
	Deletions   int       `json:"deletions"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	ReviewDecision string `json:"reviewDecision"`
	ReviewRequests struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			RequestedReviewer struct {
				Login string `json:"login"` // For User
				Name  string `json:"name"`  // For Team
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Reviews struct {
		Nodes []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State string `json:"state"`
		} `json:"nodes"`
	} `json:"reviews"`
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type model struct {
	prs               []PR
	filtered          []PR
	cursor            int
	selected          *PR
	filterMode        bool
	filterText        string
	err               error
	quitting          bool
	width             int
	height            int
	loading           bool
	refreshing        bool   // true when fetching new data while showing cached data
	confirmAction     string // non-empty while awaiting y/n confirmation (e.g. "approve")
	confirmPR         *PR
	actionPending     bool            // true while a review submission is in flight
	actionStatus      string          // transient success/error feedback for review actions
	helpMode          bool            // true while the help overlay is showing
	visibleCount      int             // for animation
	spinnerFrame      int             // for loading spinner
	refreshSeen       map[string]bool // PR keys seen during the in-flight refresh
	refreshID         int             // increments each refresh; stale page messages are dropped
	pendingShards     int             // shards still streaming pages for the current refresh
	statusFilterIndex int             // current index in statusFilters array (-1 means no filter)
	authorFilter      string          // author filter (e.g., "!username"), empty means no author filter
	repos             []string        // repo names present in m.prs, sorted; rebuilt on every refresh
	repoFilterIndex   int             // current index into repos (-1 means no repo filter)
	detail            *prDetailView   // non-nil while the full PR view is open
	cache             map[string]*prCacheEntry
	hoverSeq          int // invalidates pending prefetch ticks when the cursor moves
	warmQueue         []PR
	warmInFlight      int
	prsDirty          bool // list changed since the last cache write
	prsSaveScheduled  bool
}

type prPageLoadedMsg struct {
	prs       []PR
	endCursor string
	hasNext   bool
	shardIdx  int
	refreshID int
	err       error
}

func prKey(pr PR) string {
	return fmt.Sprintf("%s/%s#%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
}

type editorDoneMsg struct {
	action string
	pr     PR
	body   string
	err    error
}

type reviewSubmittedMsg struct {
	action string
	pr     PR
	err    error
}

type prRefreshedMsg struct {
	pr  PR
	err error
}

type metaRefreshedMsg struct {
	orgs        []string
	currentUser string
}

type tickMsg struct{}
type spinnerTickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(time.Millisecond*15, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func spinnerTick() tea.Cmd {
	return tea.Tick(time.Millisecond*80, func(t time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true)

	caretStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("33"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	selectedNormalStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15"))

	draftStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	selectedDraftStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("250"))

	approvedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))

	selectedApprovedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("114"))

	changesRequestedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196"))

	selectedChangesRequestedStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("203"))

	reviewRequestedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214"))

	selectedReviewRequestedStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("221"))

	commentedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117"))

	selectedCommentedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("159"))

	openStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	selectedOpenStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("81"))

	additionsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))

	selectedAdditionsStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("114"))

	deletionsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	selectedDeletionsStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("203"))

	filterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	branchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("141"))

	selectedBranchStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("183"))
)

func sortPRsByOldestFirst(prs []PR) {
	sort.Slice(prs, func(i, j int) bool {
		return prs[i].Number < prs[j].Number
	})
}

func initialModel() model {
	// Skip cache in demo mode
	if !demoMode {
		cached := loadCachedPRs()
		if cached != nil {
			sortPRsByOldestFirst(cached)
			return model{
				prs:               cached,
				filtered:          cached,
				cursor:            0,
				loading:           false,
				refreshing:        true,
				visibleCount:      len(cached),
				statusFilterIndex: -1,
				authorFilter:      "",
				repos:             buildRepoList(cached),
				repoFilterIndex:   -1,
				cache:             map[string]*prCacheEntry{},
			}
		}
	}
	return model{
		prs:               []PR{},
		filtered:          []PR{},
		cursor:            0,
		loading:           true,
		visibleCount:      0,
		statusFilterIndex: -1,
		authorFilter:      "",
		repos:             []string{},
		repoFilterIndex:   -1,
		cache:             map[string]*prCacheEntry{},
	}
}

func mockPRs() []PR {
	mockJSON := `[
		{"number": 142, "title": "Add user authentication flow", "headRefName": "feature/auth-flow", "isDraft": false, "additions": 847, "deletions": 123, "author": {"login": "sarah"}, "repository": {"name": "backend-api", "owner": {"login": "acme-corp"}}, "reviewDecision": "APPROVED", "reviews": {"nodes": [{"author": {"login": "mike"}, "state": "APPROVED"}]}},
		{"number": 287, "title": "Fix memory leak in worker pool", "headRefName": "fix/worker-memory", "isDraft": false, "additions": 34, "deletions": 89, "author": {"login": "alex"}, "repository": {"name": "job-runner", "owner": {"login": "acme-corp"}}, "reviewDecision": "CHANGES_REQUESTED", "reviews": {"nodes": [{"author": {"login": "sarah"}, "state": "CHANGES_REQUESTED"}]}},
		{"number": 91, "title": "Update dashboard metrics components", "headRefName": "feature/metrics-v2", "isDraft": false, "additions": 456, "deletions": 201, "author": {"login": "mike"}, "repository": {"name": "web-app", "owner": {"login": "acme-corp"}}, "reviewDecision": "REVIEW_REQUIRED", "reviewRequests": {"totalCount": 1, "nodes": [{"requestedReviewer": {"login": "alex"}}]}},
		{"number": 445, "title": "Implement rate limiting middleware", "headRefName": "feature/rate-limit", "isDraft": true, "additions": 234, "deletions": 12, "author": {"login": "jordan"}, "repository": {"name": "backend-api", "owner": {"login": "acme-corp"}}},
		{"number": 156, "title": "Add PostgreSQL connection pooling", "headRefName": "feature/pg-pool", "isDraft": false, "additions": 178, "deletions": 45, "author": {"login": "chris"}, "repository": {"name": "data-service", "owner": {"login": "acme-corp"}}, "reviewDecision": "REVIEW_REQUIRED", "reviewRequests": {"totalCount": 1, "nodes": [{"requestedReviewer": {"login": "jordan"}}]}, "reviews": {"nodes": [{"author": {"login": "alex"}, "state": "COMMENTED"}]}},
		{"number": 312, "title": "Refactor notification service", "headRefName": "refactor/notifications", "isDraft": false, "additions": 623, "deletions": 891, "author": {"login": "taylor"}, "repository": {"name": "backend-api", "owner": {"login": "acme-corp"}}, "reviewDecision": "APPROVED", "reviews": {"nodes": [{"author": {"login": "chris"}, "state": "APPROVED"}]}},
		{"number": 78, "title": "Add dark mode support", "headRefName": "feature/dark-mode", "isDraft": false, "additions": 567, "deletions": 234, "author": {"login": "sam"}, "repository": {"name": "web-app", "owner": {"login": "acme-corp"}}, "reviewDecision": "REVIEW_REQUIRED", "reviewRequests": {"totalCount": 1, "nodes": [{"requestedReviewer": {"login": "taylor"}}]}},
		{"number": 203, "title": "Upgrade to Go 1.22", "headRefName": "chore/go-upgrade", "isDraft": true, "additions": 23, "deletions": 19, "author": {"login": "alex"}, "repository": {"name": "cli-tools", "owner": {"login": "acme-corp"}}}
	]`
	var prs []PR
	json.Unmarshal([]byte(mockJSON), &prs)
	return prs
}

// findWorktreePath returns the path of a worktree checked out on the given branch, or "".
// repoPath is the main worktree root; branch is the short branch name.
func findWorktreePath(repoPath, branch string) string {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var currentPath string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		} else if line == "branch refs/heads/"+branch && currentPath != "" {
			return currentPath
		}
	}
	return ""
}

// findRepoPath searches for a repo in common locations
func findRepoPath(repoName string) string {
	home := os.Getenv("HOME")

	// Check SUP_DEV_DIR first if set
	if devDir := os.Getenv("SUP_DEV_DIR"); devDir != "" {
		path := filepath.Join(devDir, repoName)
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			return path
		}
	}

	// Search common locations
	for _, dir := range defaultDevDirs {
		var path string
		if dir == "" {
			path = filepath.Join(home, repoName)
		} else {
			path = filepath.Join(home, dir, repoName)
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			return path
		}
	}

	return ""
}

// getCurrentUser gets the authenticated GitHub username
func getCurrentUser() string {
	cmd := exec.Command("gh", "api", "user", "--jq", ".login")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// fetchUserOrgs gets the list of organizations the user belongs to
func fetchUserOrgs() ([]string, error) {
	cmd := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch orgs: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var result []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result, nil
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

func fetchShardPage(shardIdx int, after string, refreshID int) tea.Cmd {
	return func() tea.Msg {
		if demoMode {
			return prPageLoadedMsg{prs: mockPRs(), shardIdx: shardIdx, refreshID: refreshID}
		}

		shards := searchShards()
		if shardIdx >= len(shards) {
			return prPageLoadedMsg{shardIdx: shardIdx, refreshID: refreshID}
		}

		afterArg := "null"
		if after != "" {
			afterArg = `"` + after + `"`
		}

		query := fmt.Sprintf(`{
			search(query: "%s", type: ISSUE, first: 50, after: %s) {
				pageInfo { endCursor hasNextPage }
				nodes {
					... on PullRequest {
						number
						title
						headRefName
						headRefOid
						updatedAt
						isDraft
						additions
						deletions
						author { login }
						repository { name owner { login } }
						reviewDecision
						reviewRequests(first: 5) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
						reviews(last: 5) { nodes { author { login } state } }
					}
				}
			}
		}`, shards[shardIdx], afterArg)

		output, err := graphqlPOST(query)
		if err != nil {
			return prPageLoadedMsg{shardIdx: shardIdx, refreshID: refreshID, err: fmt.Errorf("failed to fetch PRs: %w", err)}
		}

		var resp struct {
			Data struct {
				Search struct {
					PageInfo struct {
						EndCursor   string `json:"endCursor"`
						HasNextPage bool   `json:"hasNextPage"`
					} `json:"pageInfo"`
					Nodes []PR `json:"nodes"`
				} `json:"search"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output, &resp); err != nil {
			return prPageLoadedMsg{shardIdx: shardIdx, refreshID: refreshID, err: fmt.Errorf("failed to parse PRs: %w", err)}
		}

		return prPageLoadedMsg{
			prs:       resp.Data.Search.Nodes,
			endCursor: resp.Data.Search.PageInfo.EndCursor,
			hasNext:   resp.Data.Search.PageInfo.HasNextPage,
			shardIdx:  shardIdx,
			refreshID: refreshID,
		}
	}
}

// startRefresh resets refresh state on the model and returns the batch of
// commands that drive the parallel fan-out: one chain per search shard plus
// one batched-aliases GraphQL request per chunk of already-cached PRs.
func (m *model) startRefresh() tea.Cmd {
	m.refreshID++
	m.refreshSeen = make(map[string]bool)
	shards := searchShards()
	if len(shards) == 0 {
		m.refreshing = false
		return nil
	}
	m.pendingShards = len(shards)
	cmds := make([]tea.Cmd, 0, len(shards)+4)
	for i := range shards {
		cmds = append(cmds, fetchShardPage(i, "", m.refreshID))
	}

	// Fast path: refresh cached PRs in aliased batches, in parallel, so rows
	// update as each batch lands. Batches of 10 measured fastest (117 PRs:
	// ~1.7s vs ~2.4s one-per-PR, and 12 requests instead of 117).
	if !demoMode {
		for i := 0; i < len(m.prs); i += prRefreshBatch {
			cmds = append(cmds, fetchPRBatchCmd(m.prs[i:min(i+prRefreshBatch, len(m.prs))]))
		}
	}

	cmds = append(cmds, spinnerTick())
	return tea.Batch(cmds...)
}

func submitReviewCmd(action string, pr PR, body string) tea.Cmd {
	return func() tea.Msg {
		repoSlug := fmt.Sprintf("%s/%s", pr.Repository.Owner.Login, pr.Repository.Name)
		args := []string{"pr", "review", fmt.Sprintf("%d", pr.Number), "--repo", repoSlug}
		switch action {
		case "approve":
			args = append(args, "--approve")
		case "request-changes":
			args = append(args, "--request-changes", "--body", body)
		case "comment":
			args = append(args, "--comment", "--body", body)
		}
		cmd := exec.Command("gh", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return reviewSubmittedMsg{action: action, pr: pr, err: fmt.Errorf("%s", msg)}
		}
		return reviewSubmittedMsg{action: action, pr: pr}
	}
}

// prRowFields are the PR fields a list row needs.
const prRowFields = `number title headRefName headRefOid updatedAt isDraft additions deletions
	author { login } reviewDecision
	reviewRequests(first: 5) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
	reviews(last: 5) { nodes { author { login } state } }`

const prRefreshBatch = 10

type prsRefreshedMsg struct {
	prs []PR
	err error
}

// fetchPRBatchCmd refreshes several PRs in one GraphQL request using aliases.
func fetchPRBatchCmd(prs []PR) tea.Cmd {
	return func() tea.Msg {
		var q strings.Builder
		q.WriteString("{")
		for i, p := range prs {
			fmt.Fprintf(&q, "p%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { %s } }\n",
				i, p.Repository.Owner.Login, p.Repository.Name, p.Number, prRowFields)
		}
		q.WriteString("}")
		out, err := graphqlPOST(q.String())
		if err != nil {
			return prsRefreshedMsg{err: err}
		}
		var resp struct {
			Data map[string]*struct {
				PullRequest *PR `json:"pullRequest"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return prsRefreshedMsg{err: err}
		}
		var updated []PR
		for i, p := range prs {
			// A PR deleted or made inaccessible comes back null; the search refresh prunes it.
			r := resp.Data[fmt.Sprintf("p%d", i)]
			if r == nil || r.PullRequest == nil {
				continue
			}
			u := *r.PullRequest
			u.Repository = p.Repository
			updated = append(updated, u)
		}
		return prsRefreshedMsg{prs: updated}
	}
}

func fetchSinglePRCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		msg := fetchPRBatchCmd([]PR{pr})().(prsRefreshedMsg)
		switch {
		case msg.err != nil:
			return prRefreshedMsg{err: msg.err}
		case len(msg.prs) == 0:
			return prRefreshedMsg{err: fmt.Errorf("PR #%d not found", pr.Number)}
		}
		return prRefreshedMsg{pr: msg.prs[0]}
	}
}

func startEditorCmd(action string, pr PR) (tea.Cmd, error) {
	f, err := os.CreateTemp("", fmt.Sprintf("sup-review-%d-*.md", pr.Number))
	if err != nil {
		return nil, err
	}
	tmpPath := f.Name()
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	editorCmd := exec.Command(editor, tmpPath)
	return tea.ExecProcess(editorCmd, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return editorDoneMsg{action: action, pr: pr, err: err}
		}
		body, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			return editorDoneMsg{action: action, pr: pr, err: readErr}
		}
		return editorDoneMsg{action: action, pr: pr, body: strings.TrimSpace(string(body))}
	}), nil
}

func refreshMetaCmd() tea.Msg {
	if demoMode {
		return metaRefreshedMsg{orgs: orgs, currentUser: currentUser}
	}
	newOrgs := orgs
	if !mineMode && os.Getenv("SUP_ORG") == "" {
		if fetched, err := fetchUserOrgs(); err == nil && len(fetched) > 0 {
			newOrgs = fetched
		}
	}
	newUser := getCurrentUser()
	if newUser == "" {
		newUser = currentUser
	}
	saveCachedMeta(metaCache{Orgs: newOrgs, CurrentUser: newUser})
	return metaRefreshedMsg{orgs: newOrgs, currentUser: newUser}
}

type startRefreshMsg struct{}

func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return startRefreshMsg{} }, refreshMetaCmd)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(kittyFlushedMsg); ok {
		handleKittyFlushed(f)
		return m, scheduleKittyFlush()
	}
	next, cmd := m.update(msg)
	if nm := next.(model); nm.detail != nil {
		// Panes render lazily, only once their tab is showing.
		nm.detail.ensure()
		nm.detail.clampScroll(nm.height)
	}
	// Any rebuild may have queued image transmissions for the next frame.
	return next, tea.Batch(cmd, scheduleKittyFlush())
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.detail != nil {
			m.detail.rebuild(m.width)
			m.detail.clampScroll(m.height)
		}
		return m, nil

	case prPatchFetchedMsg:
		return m, m.handlePatchFetched(msg)

	case patchParsedMsg:
		return m, m.handlePatchParsed(msg)

	case prDetailFetchedMsg:
		return m, m.handleDetailFetched(msg, true)

	case diskCacheLoadedMsg:
		return m, m.handleDiskCacheLoaded(msg, true)

	case imagesLoadedMsg:
		handleImagesLoaded(msg)
		if m.detail != nil {
			m.detail.rebuildForImages()
			m.detail.clampScroll(m.height)
		}
		return m, nil

	case hoverMsg:
		return m, m.handleHover(msg)

	case reviewerCandidatesMsg:
		if v := m.detail; v != nil && v.picker != nil && prKey(v.pr) == msg.key {
			if msg.err != nil {
				v.picker.loading, v.picker.err = false, msg.err
			} else {
				latest, _ := latestReviewStates(v.detail)
				v.picker.setCandidates(msg.cands, v.pr.Author.Login, latest)
			}
		}
		return m, nil

	case reviewersSavedMsg:
		return m, m.handleReviewersSaved(msg)

	case mergeDoneMsg:
		return m, m.handleMergeDone(msg)

	case startRefreshMsg:
		return m, tea.Batch(m.startRefresh(), m.schedulePrefetch(), m.enqueueWarm())

	case warmDoneMsg:
		return m, m.handleWarmDone(msg)

	case prPageLoadedMsg:
		// Stale message from a prior refresh — ignore.
		if msg.refreshID != m.refreshID {
			return m, nil
		}
		if msg.err != nil {
			if len(m.prs) == 0 {
				m.err = msg.err
			}
			m.pendingShards--
			if m.pendingShards <= 0 {
				m.refreshing = false
				m.loading = false
			}
			return m, nil
		}

		index := make(map[string]int, len(m.prs))
		for i := range m.prs {
			index[prKey(m.prs[i])] = i
		}
		for _, np := range msg.prs {
			key := prKey(np)
			m.refreshSeen[key] = true
			if i, ok := index[key]; ok {
				m.prs[i] = np
			} else {
				index[key] = len(m.prs)
				m.prs = append(m.prs, np)
			}
		}

		// If this shard has more pages, fire the next page now (don't wait for
		// other shards). Otherwise this shard is done.
		var next tea.Cmd
		if msg.hasNext {
			next = fetchShardPage(msg.shardIdx, msg.endCursor, msg.refreshID)
		} else {
			m.pendingShards--
		}

		// When every shard has finished, prune PRs not seen this refresh and persist.
		if m.pendingShards <= 0 && !msg.hasNext {
			kept := m.prs[:0]
			for _, pr := range m.prs {
				if m.refreshSeen[prKey(pr)] {
					kept = append(kept, pr)
				}
			}
			m.prs = kept
			m.refreshing = false
		}

		sortPRsByOldestFirst(m.prs)
		m.syncRepoList()
		m.applyFilterKeepingCursor()

		m.loading = false
		m.visibleCount = len(m.filtered)

		// Fresh updatedAt/head values: re-warm anything that changed.
		if !m.refreshing {
			return m, tea.Batch(next, m.enqueueWarm(), m.markPRsDirty())
		}
		return m, next

	case savePRsMsg:
		m.prsSaveScheduled = false
		if m.prsDirty {
			m.prsDirty = false
			savePRsToCache(m.prs)
		}
		return m, nil

	case tickMsg:
		if m.visibleCount < len(m.filtered) {
			m.visibleCount += 2 // animate 2 at a time for speed
			if m.visibleCount > len(m.filtered) {
				m.visibleCount = len(m.filtered)
			}
			return m, tick()
		}
		return m, nil

	case spinnerTickMsg:
		if m.loading || m.refreshing || m.actionPending || (m.detail != nil && (m.detail.loading || m.detail.diffLoading ||
			(m.detail.picker != nil && (m.detail.picker.loading || m.detail.picker.saving)) ||
			(m.detail.merge != nil && m.detail.merge.saving))) {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerTick()
		}
		return m, nil

	case editorDoneMsg:
		if msg.err != nil {
			m.actionStatus = "Error: " + msg.err.Error()
			return m, nil
		}
		if msg.body == "" {
			m.actionStatus = fmt.Sprintf("%s cancelled (empty body)", msg.action)
			return m, nil
		}
		m.actionPending = true
		return m, tea.Batch(submitReviewCmd(msg.action, msg.pr, msg.body), spinnerTick())

	case reviewSubmittedMsg:
		m.actionPending = false
		if msg.err != nil {
			m.actionStatus = "Error: " + msg.err.Error()
			return m, nil
		}
		var verb string
		switch msg.action {
		case "approve":
			verb = "Approved"
		case "request-changes":
			verb = "Requested changes on"
		case "comment":
			verb = "Commented on"
		}
		m.actionStatus = fmt.Sprintf("✓ %s PR #%d", verb, msg.pr.Number)
		// Refresh just this PR so its badge reflects the new review state.
		cmds := []tea.Cmd{fetchSinglePRCmd(msg.pr)}
		if m.detail != nil && prKey(m.detail.pr) == prKey(msg.pr) {
			m.detail.loading = true
			cmds = append(cmds, fetchPRDetailCmd(msg.pr), spinnerTick())
		}
		return m, tea.Batch(cmds...)

	case metaRefreshedMsg:
		if len(msg.orgs) > 0 {
			orgs = msg.orgs
		}
		if msg.currentUser != "" {
			currentUser = msg.currentUser
		}
		return m, nil

	case prRefreshedMsg:
		if msg.err != nil {
			// Best-effort; leave the list alone on failure.
			return m, nil
		}
		return m, m.applyRefreshedPRs([]PR{msg.pr})

	case prsRefreshedMsg:
		if msg.err != nil {
			return m, nil
		}
		return m, m.applyRefreshedPRs(msg.prs)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		// ctrl+c always quits, even from the help overlay.
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.detail != nil {
			return m.handleDetailInput(msg)
		}
		// In help overlay, only ?/esc/q dismiss; everything else is ignored.
		if m.helpMode {
			switch msg.String() {
			case "?", "esc", "q":
				m.helpMode = false
			}
			return m, nil
		}
		// Allow quitting even during animation
		if msg.String() == "q" {
			m.quitting = true
			return m, tea.Quit
		}
		// Skip animation on any key press
		if m.visibleCount < len(m.filtered) {
			m.visibleCount = len(m.filtered)
			return m, nil
		}
		if m.filterMode {
			return m.handleFilterInput(msg)
		}
		// Esc clears an active filter first; only quits when nothing to clear.
		if msg.String() == "esc" {
			if m.authorFilter != "" || m.statusFilterIndex >= 0 || m.filterText != "" || m.repoFilterIndex >= 0 {
				m.authorFilter = ""
				m.statusFilterIndex = -1
				m.repoFilterIndex = -1
				m.filterText = ""
				m.applyFilter()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		}
		return m.handleNormalInput(msg)
	}

	return m, nil
}

func (m model) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filterMode = false
		m.filterText = ""
		m.applyFilter()
		return m, nil

	case tea.KeyEnter:
		m.filterMode = false
		return m, nil

	case tea.KeyBackspace:
		if len(m.filterText) > 0 {
			m.filterText = m.filterText[:len(m.filterText)-1]
			m.applyFilter()
		}
		return m, nil

	default:
		switch msg.String() {
		case "backspace", "ctrl+h", "del":
			if len(m.filterText) > 0 {
				m.filterText = m.filterText[:len(m.filterText)-1]
				m.applyFilter()
			}
			return m, nil
		}
		if msg.Type == tea.KeyRunes {
			m.filterText += string(msg.Runes)
			m.applyFilter()
		}
		return m, nil
	}
}

// prFilter is the resolved set of active constraints. A zero-valued field means
// that dimension is unconstrained; every non-empty field must match (AND).
type prFilter struct {
	repo     string // exact repo name match
	author   string // substring of pr.Author.Login
	reviewer string // substring of the requested reviewer names
	status   string // exact statusLabelForFilter match
	text     string // substring across all searchable fields
}

func (f prFilter) matches(pr PR) bool {
	if f.repo != "" && strings.ToLower(pr.Repository.Name) != f.repo {
		return false
	}
	if f.author != "" && !strings.Contains(strings.ToLower(pr.Author.Login), f.author) {
		return false
	}
	if f.reviewer != "" {
		requested := strings.ToLower(getRequestedReviewerNames(pr))
		if requested == "" || !strings.Contains(requested, f.reviewer) {
			return false
		}
	}
	if f.status != "" && statusLabelForFilter(pr) != f.status {
		return false
	}
	if f.text != "" {
		searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s %s %s #%d %d %s",
			pr.Repository.Name, pr.Title, pr.Author.Login, pr.HeadRefName, statusLabelForFilter(pr),
			pr.Repository.Owner.Login, pr.Number, pr.Number, getAllReviewerNames(pr)))
		if !strings.Contains(searchText, f.text) {
			return false
		}
	}
	return true
}

// buildFilter resolves model state into a single constraint set. Text-box
// prefixes (#repo, @reviewer, !author) override the equivalent cycled filter.
func (m *model) buildFilter() prFilter {
	f := prFilter{}

	if r := m.currentRepo(); r != "" {
		f.repo = strings.ToLower(r)
	}
	if m.statusFilterIndex >= 0 && m.statusFilterIndex < len(statusFilters) {
		f.status = statusFilters[m.statusFilterIndex]
	}
	switch {
	case strings.HasPrefix(m.authorFilter, "!"):
		f.author = strings.ToLower(strings.TrimPrefix(m.authorFilter, "!"))
	case strings.HasPrefix(m.authorFilter, "@"):
		f.reviewer = strings.ToLower(strings.TrimPrefix(m.authorFilter, "@"))
	}

	switch t := strings.ToLower(strings.TrimSpace(m.filterText)); {
	case t == "":
	case strings.HasPrefix(t, "#"):
		f.repo = strings.TrimPrefix(t, "#")
	case strings.HasPrefix(t, "@"):
		f.reviewer = strings.TrimPrefix(t, "@")
	case strings.HasPrefix(t, "!"):
		f.author = strings.TrimPrefix(t, "!")
	default:
		// Typing a status name drives the status cycle rather than a text search,
		// so the header and the `s` key stay in sync with what is displayed.
		if i := indexOfStatus(t); i >= 0 {
			m.statusFilterIndex = i
			f.status = statusFilters[i]
		} else {
			f.text = t
		}
	}
	return f
}

func (m *model) applyFilter() {
	f := m.buildFilter()
	m.filtered = nil
	for _, pr := range m.prs {
		if f.matches(pr) {
			m.filtered = append(m.filtered, pr)
		}
	}
	m.cursor = 0
}

// applyFilterKeepingCursor re-filters after data changes without moving the selection.
// applyRefreshedPRs swaps updated rows in, re-filtering once per batch.
func (m *model) applyRefreshedPRs(updated []PR) tea.Cmd {
	for _, u := range updated {
		for i := range m.prs {
			if samePR(m.prs[i], u) {
				m.prs[i] = u
				break
			}
		}
		if m.detail != nil && samePR(m.detail.pr, u) {
			m.detail.pr = u
		}
	}
	// Every active filter (repo, status, author) must survive the rebuild, not just typed text.
	m.applyFilterKeepingCursor()
	return m.markPRsDirty()
}

func (m *model) applyFilterKeepingCursor() {
	var selected *PR
	if m.cursor < len(m.filtered) {
		pr := m.filtered[m.cursor]
		selected = &pr
	}
	m.applyFilter()
	if selected == nil {
		return
	}
	for i, pr := range m.filtered {
		if samePR(pr, *selected) {
			m.cursor = i
			break
		}
	}
}

func indexOfStatus(name string) int {
	for i, s := range statusFilters {
		if name == s {
			return i
		}
	}
	return -1
}

// currentRepo returns the pinned repo name, or "" when no repo filter is active.
func (m *model) currentRepo() string {
	if m.repoFilterIndex < 0 || m.repoFilterIndex >= len(m.repos) {
		return ""
	}
	return m.repos[m.repoFilterIndex]
}

// buildRepoList collects the unique repo names across prs, sorted for stable cycling.
func buildRepoList(prs []PR) []string {
	seen := make(map[string]bool, len(prs))
	var repos []string
	for _, pr := range prs {
		if name := pr.Repository.Name; name != "" && !seen[name] {
			seen[name] = true
			repos = append(repos, name)
		}
	}
	sort.Strings(repos)
	return repos
}

// syncRepoList rebuilds the repo list after a refresh, keeping the pinned repo
// pinned even though its index may have shifted (or dropped out entirely).
func (m *model) syncRepoList() {
	pinned := m.currentRepo()
	m.repos = buildRepoList(m.prs)
	m.repoFilterIndex = -1
	if pinned == "" {
		return
	}
	for i, r := range m.repos {
		if r == pinned {
			m.repoFilterIndex = i
			return
		}
	}
}

// cycleRepoFilter steps the repo filter by delta, passing through an
// unfiltered position so the cycle can always be cleared.
func (m *model) cycleRepoFilter(delta int) {
	if len(m.repos) == 0 {
		return
	}
	n := len(m.repos) + 1 // positions 0..len-1 are repos, len is "no filter"
	pos := m.repoFilterIndex
	if pos < 0 {
		pos = len(m.repos)
	}
	pos = ((pos+delta)%n + n) % n
	if pos == len(m.repos) {
		m.repoFilterIndex = -1
	} else {
		m.repoFilterIndex = pos
	}
	m.applyFilter()
}

// currentPR is the PR that actions target: the open detail view's PR, else the cursor row.
func (m model) currentPR() (PR, bool) {
	if m.detail != nil {
		return m.detail.pr, true
	}
	if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
		return m.filtered[m.cursor], true
	}
	return PR{}, false
}

func (m model) handleNormalInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Confirmation prompts intercept input before any other handling.
	if m.confirmAction != "" {
		switch msg.String() {
		case "y", "Y":
			action := m.confirmAction
			pr := *m.confirmPR
			m.confirmAction = ""
			m.confirmPR = nil
			m.actionPending = true
			m.actionStatus = ""
			return m, tea.Batch(submitReviewCmd(action, pr, ""), spinnerTick())
		default:
			m.confirmAction = ""
			m.confirmPR = nil
		}
		return m, nil
	}

	// Filter cycling keys run before the transient-feedback reset below.
	switch msg.String() {
	case "s":
		m.cycleStatusFilter()
		return m, nil
	case "p":
		m.cycleRepoFilter(1)
		return m, nil
	case "P":
		m.cycleRepoFilter(-1)
		return m, nil
	}

	// Any key dismisses transient feedback from a previous action.
	m.actionStatus = ""

	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.schedulePrefetch()

	case "down", "j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, m.schedulePrefetch()

	case "g":
		m.cursor = 0
		return m, m.schedulePrefetch()

	case "G":
		if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
		return m, m.schedulePrefetch()

	case "a":
		if currentUser != "" {
			if m.authorFilter == "!"+currentUser {
				// Toggle off same filter
				m.authorFilter = ""
			} else {
				// Set author filter
				m.authorFilter = "!" + currentUser
			}
			m.applyFilter()
		}
		return m, nil

	case "r":
		if currentUser != "" {
			if m.authorFilter == "@"+currentUser {
				// Toggle off same filter
				m.authorFilter = ""
			} else {
				// Set reviewer filter
				m.authorFilter = "@" + currentUser
			}
			m.applyFilter()
		}
		return m, nil

	case "R":
		m.refreshing = true
		return m, m.startRefresh()

	case "/":
		m.filterMode = true
		m.filterText = ""
		return m, nil

	case "?":
		m.helpMode = true
		return m, nil

	case "enter":
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			m.selected = &m.filtered[m.cursor]
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case "v":
		if pr, ok := m.currentPR(); ok {
			return m, m.openDetail(pr)
		}
		return m, nil

	case "m":
		if pr, ok := m.currentPR(); ok && m.detail == nil {
			cmd := m.openDetail(pr)
			m.detail.merge = newMergeDialog(m.detail.detail)
			return m, cmd
		}
		return m, nil

	case "d":
		// Opens the PR view straight on its diff.
		if pr, ok := m.currentPR(); ok && m.detail == nil {
			cmd := m.openDetail(pr)
			m.detail.tab = tabFiles
			return m, cmd
		}
		return m, nil

	case "A":
		if m.actionPending {
			return m, nil
		}
		if pr, ok := m.currentPR(); ok {
			m.confirmAction = "approve"
			m.confirmPR = &pr
		}
		return m, nil

	case "D":
		if m.actionPending {
			return m, nil
		}
		if pr, ok := m.currentPR(); ok {
			cmd, err := startEditorCmd("request-changes", pr)
			if err != nil {
				m.actionStatus = "Error: " + err.Error()
				return m, nil
			}
			return m, cmd
		}
		return m, nil

	case "M":
		if m.actionPending {
			return m, nil
		}
		if pr, ok := m.currentPR(); ok {
			cmd, err := startEditorCmd("comment", pr)
			if err != nil {
				m.actionStatus = "Error: " + err.Error()
				return m, nil
			}
			return m, cmd
		}
		return m, nil

	case "o":
		if pr, ok := m.currentPR(); ok {
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
			exec.Command("open", "-g", url).Start()
		}
		return m, nil

	case "c":
		if pr, ok := m.currentPR(); ok {
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
			cmd := exec.Command("pbcopy")
			cmd.Stdin = strings.NewReader(url)
			if err := cmd.Run(); err != nil {
				m.actionStatus = "Error copying link: " + err.Error()
			} else {
				m.actionStatus = fmt.Sprintf("✓ Copied PR #%d link", pr.Number)
			}
		}
		return m, nil

	case "O":
		for _, pr := range m.filtered {
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
			exec.Command("open", "-g", url).Start()
		}
		return m, nil
	}

	return m, nil
}

var statusFilters = []string{"draft", "approved", "denied", "review", "commented", "open"}

func (m *model) cycleStatusFilter() {
	n := len(statusFilters) + 1 // last position is "no filter"
	pos := m.statusFilterIndex
	if pos < 0 {
		pos = len(statusFilters)
	}
	pos = (pos + 1) % n
	if pos == len(statusFilters) {
		m.statusFilterIndex = -1
	} else {
		m.statusFilterIndex = pos
	}
	// A typed status name feeds the same cycle, so drop it on the way past.
	if indexOfStatus(strings.ToLower(m.filterText)) >= 0 {
		m.filterText = ""
	}
	m.applyFilter()
}

// Status kinds index the badge painters; order matches statusKind.
var (
	badgeLabels    = [6]string{"[Draft]", "[Approved]", "[Denied]", "[Commented]", "[Review]", "[Open]"}
	badgeColors    = [6]string{"241", "78", "196", "117", "214", "39"}
	badgeSelColors = [6]string{"250", "114", "203", "159", "221", "81"}
)

func statusKind(pr PR) int {
	if pr.IsDraft {
		return 0
	}
	switch pr.ReviewDecision {
	case "APPROVED":
		return 1
	case "CHANGES_REQUESTED":
		return 2
	case "REVIEW_REQUIRED":
		if len(pr.Reviews.Nodes) > 0 && pr.Reviews.Nodes[0].State == "COMMENTED" {
			return 3
		}
		return 4
	}
	return 5
}

func getStatusBadge(pr PR) string {
	k := statusKind(pr)
	return sty(badgeColors[k], "", false, false).Render(badgeLabels[k])
}

func getSelectedStatusBadge(pr PR) string {
	k := statusKind(pr)
	return sty(badgeSelColors[k], "", true, false).Render(badgeLabels[k])
}

func statusLabelForFilter(pr PR) string {
	if pr.IsDraft {
		return "draft"
	}

	switch pr.ReviewDecision {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "denied"
	case "REVIEW_REQUIRED":
		if len(pr.Reviews.Nodes) > 0 && pr.Reviews.Nodes[0].State == "COMMENTED" {
			return "commented"
		}
		return "review"
	default:
		return "open"
	}
}

// statusBadgeLabel is the display text of the list status badge.
func statusBadgeLabel(pr PR) string { return badgeLabels[statusKind(pr)] }

func getReviewer(pr PR) string {
	// First check requested reviewers
	if len(pr.ReviewRequests.Nodes) > 0 {
		reviewer := pr.ReviewRequests.Nodes[0].RequestedReviewer
		name := reviewer.Login
		if name == "" {
			name = reviewer.Name
		}
		if name != "" {
			return name
		}
	}
	// Fall back to last reviewer who commented/reviewed
	if len(pr.Reviews.Nodes) > 0 {
		return pr.Reviews.Nodes[0].Author.Login
	}
	return ""
}

func getRequestedReviewerNames(pr PR) string {
	var names []string
	for _, rr := range pr.ReviewRequests.Nodes {
		name := rr.RequestedReviewer.Login
		if name == "" {
			name = rr.RequestedReviewer.Name
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, " ")
}

func getAllReviewerNames(pr PR) string {
	var names []string
	for _, rr := range pr.ReviewRequests.Nodes {
		name := rr.RequestedReviewer.Login
		if name == "" {
			name = rr.RequestedReviewer.Name
		}
		if name != "" {
			names = append(names, name)
		}
	}
	for _, r := range pr.Reviews.Nodes {
		if r.Author.Login != "" {
			names = append(names, r.Author.Login)
		}
	}
	return strings.Join(names, " ")
}

func getDiffStats(pr PR) string {
	adds := additionsStyle.Render(fmt.Sprintf("+%d", pr.Additions))
	dels := deletionsStyle.Render(fmt.Sprintf("-%d", pr.Deletions))
	return fmt.Sprintf("%s %s", adds, dels)
}

func (m model) helpView() string {
	var s strings.Builder
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	sections := []struct {
		title string
		items [][2]string
	}{
		{"Navigation", [][2]string{
			{"j / ↓", "Move down"},
			{"k / ↑", "Move up"},
			{"g / G", "Top / bottom"},
		}},
		{"Filter", [][2]string{
			{"/", "Open filter"},
			{"s", "Cycle status filter"},
			{"p / P", "Cycle repo (fwd / back)"},
			{"#repo", "Filter by repo"},
			{"@user", "Filter by reviewer"},
			{"!user", "Filter by author"},
			{"a", "My PRs"},
			{"r", "My reviews"},
		}},
		{"Actions", [][2]string{
			{"Enter", "Checkout PR"},
			{"v", "Full PR view (conversation + highlighted diff)"},
			{"m", "Merge PR"},
			{"d", "Open the PR's diff"},
			{"A", "Approve"},
			{"D", "Request changes"},
			{"M", "Comment"},
			{"o", "Open in browser"},
			{"O", "Open all needing review"},
			{"c", "Copy PR link"},
		}},
		{"Other", [][2]string{
			{"R", "Refresh PR list"},
			{"?", "Toggle this help"},
			{"esc", "Clear filter (or quit)"},
			{"q", "Quit"},
		}},
	}

	const keyCol = 12
	s.WriteString("\n  " + headerStyle.Render("sup — keybindings") + "\n\n")
	for _, sec := range sections {
		s.WriteString("  " + sectionStyle.Render(sec.title) + "\n")
		for _, item := range sec.items {
			s.WriteString("    " + keyStyle.Render(pad(item[0], keyCol)) + descStyle.Render(item[1]) + "\n")
		}
		s.WriteString("\n")
	}
	s.WriteString("  " + dimStyle.Render("? · esc · q to close") + "\n")
	return s.String()
}

// activeFilterLabels describes each active filter for the header line.
func (m model) activeFilterLabels() []string {
	var parts []string
	if repo := m.currentRepo(); repo != "" {
		parts = append(parts, fmt.Sprintf("#%s (%d/%d)", repo, m.repoFilterIndex+1, len(m.repos)))
	}
	if m.authorFilter != "" {
		parts = append(parts, m.authorFilter)
	}
	if m.statusFilterIndex >= 0 && m.statusFilterIndex < len(statusFilters) {
		parts = append(parts, statusFilters[m.statusFilterIndex])
	}
	if m.filterText != "" && indexOfStatus(strings.ToLower(m.filterText)) < 0 && !strings.HasPrefix(m.filterText, "#") {
		parts = append(parts, m.filterText)
	}
	return parts
}

func (m model) View() string {
	// Pending kitty image transmissions ride along at the start of the frame;
	// they're zero-width, and drop out once flushed.
	return strings.Join(kittyPending, "") + m.view()
}

func (m model) view() string {
	if m.detail != nil {
		return m.detailView()
	}
	if m.helpMode {
		return m.helpView()
	}
	var s strings.Builder
	lp := newListPainters()

	// Fixed column widths
	const (
		colStatus   = 12
		colNum      = 6
		colAuthor   = 14
		colReviewer = 14
		colDiff     = 16 // approximate width for +/- stats
		colPadding  = 4  // cursor + spacing
	)

	// Calculate dynamic column widths based on terminal width
	fixedWidth := colStatus + colNum + colAuthor + colReviewer + colDiff + colPadding
	flexWidth := m.width - fixedWidth
	if flexWidth < 60 {
		flexWidth = 60 // minimum for flexible columns
	}

	// Distribute flexible space: 30% repo, 40% title, 30% branch
	colRepo := flexWidth * 30 / 100
	colTitle := flexWidth * 40 / 100
	colBranch := flexWidth - colRepo - colTitle

	s.WriteString("\n")
	separatorWidth := m.width - 2 // account for "  " prefix
	if separatorWidth < 60 {
		separatorWidth = 60
	}
	rowWidth := separatorWidth

	filterLine := "  "
	if m.filterMode {
		filterLine = fmt.Sprintf("  / %s█", m.filterText)
	} else if parts := m.activeFilterLabels(); len(parts) > 0 {
		filterLine = "  Filter: " + strings.Join(parts, " ")
	}
	switch {
	case m.actionPending:
		spinner := spinnerFrames[m.spinnerFrame]
		s.WriteString(lp.loading.Render("  " + spinner + " Submitting review..."))
	case m.actionStatus != "":
		style := approvedStyle
		if strings.HasPrefix(m.actionStatus, "Error") || strings.Contains(m.actionStatus, "cancelled") {
			style = changesRequestedStyle
		}
		s.WriteString(style.Render("  " + m.actionStatus))
	case filterLine == "  ":
		s.WriteString(filterLine)
	default:
		s.WriteString(lp.filter.Render(filterLine))
	}
	s.WriteString("\n")

	// Always show header
	s.WriteString(lp.dim.Render("  " + pad("STATUS", colStatus) + pad("REPO", colRepo) + pad("#", colNum) + pad("TITLE", colTitle) + pad("AUTHOR", colAuthor) + pad("REVIEWER", colReviewer) + pad("BRANCH", colBranch) + "+/-"))
	s.WriteString("\n")
	s.WriteString(lp.dim.Render("  " + strings.Repeat("─", separatorWidth)))
	s.WriteString("\n")

	if m.err != nil {
		s.WriteString(fmt.Sprintf("\n  Error: %v\n", m.err))
		s.WriteString(helpStyle.Render("\n  Press q to quit.\n"))
		return s.String()
	}

	if m.loading {
		spinner := spinnerFrames[m.spinnerFrame]
		s.WriteString(lp.loading.Render("  " + spinner + " Loading..."))
		s.WriteString("\n")
		return s.String()
	}

	if len(m.filtered) == 0 {
		s.WriteString("  No PRs found.\n")
	} else {
		// Calculate visible range
		visibleItems := m.height - 8
		if visibleItems < 5 {
			visibleItems = 15
		}

		start := 0
		if m.cursor >= visibleItems {
			start = m.cursor - visibleItems + 1
		}
		end := start + visibleItems
		if end > len(m.filtered) {
			end = len(m.filtered)
		}
		// Limit by animation progress
		if end > m.visibleCount {
			end = m.visibleCount
		}

		for i := start; i < end; i++ {
			pr := m.filtered[i]
			isSelected := m.cursor == i

			cursor := "  "
			if isSelected {
				cursor = "» "
			}

			kind := statusKind(pr)
			label := badgeLabels[kind]
			repo, repoW := padW(truncate(pr.Repository.Name, colRepo-1), colRepo)
			num, numW := padW("#"+strconv.Itoa(pr.Number), colNum)
			title, titleW := padW(truncate(pr.Title, colTitle-1), colTitle)
			author, authorW := padW(truncate(pr.Author.Login, colAuthor-1), colAuthor)
			reviewer, reviewerW := padW(truncate(getReviewer(pr), colReviewer-1), colReviewer)
			branch, branchW := padW(truncate(pr.HeadRefName, colBranch-1), colBranch)
			leftDiff := (colDiff - 1) / 2
			rightDiff := colDiff - 1 - leftDiff
			addsPadded, addsW := padLeftW("+"+strconv.Itoa(pr.Additions), leftDiff)
			delsPadded, delsW := padLeftW("-"+strconv.Itoa(pr.Deletions), rightDiff)
			statusW := displayWidth(label)
			if statusW < colStatus {
				statusW = colStatus
			}
			currentWidth := len(cursor) + statusW + repoW + numW + titleW + authorW + reviewerW + branchW + addsW + 1 + delsW
			statusFill := ""
			if n := colStatus - len(label); n > 0 {
				statusFill = spaces(n)
			}

			if isSelected {
				s.WriteString(lp.caret.Render(cursor))
				s.WriteString(lp.selBadge[kind].Render(label) + statusFill)
				s.WriteString(lp.selNormal.Render(repo))
				s.WriteString(lp.selNormal.Render(num))
				s.WriteString(lp.selNormal.Render(title))
				s.WriteString(lp.selAuthor.Render(author))
				s.WriteString(lp.selReviewer.Render(reviewer))
				s.WriteString(lp.selBranch.Render(branch))
				s.WriteString(lp.selAdds.Render(addsPadded))
				s.WriteString(" ")
				s.WriteString(lp.selDels.Render(delsPadded))
			} else {
				s.WriteString(cursor)
				s.WriteString(lp.badge[kind].Render(label) + statusFill)
				s.WriteString(lp.normal.Render(repo))
				s.WriteString(lp.normal.Render(num))
				s.WriteString(lp.normal.Render(title))
				s.WriteString(lp.dim.Render(author))
				s.WriteString(lp.reviewer.Render(reviewer))
				s.WriteString(lp.branch.Render(branch))
				s.WriteString(lp.adds.Render(addsPadded))
				s.WriteString(" ")
				s.WriteString(lp.dels.Render(delsPadded))
			}
			if currentWidth < rowWidth {
				s.WriteString(spaces(rowWidth - currentWidth))
			}
			s.WriteString("\n")
		}

		// Any filter (repo, status, author, text) narrows the list, so count what's shown.
		if len(m.filtered) != len(m.prs) {
			s.WriteString(fmt.Sprintf("\n  %d of %d PRs", len(m.filtered), len(m.prs)))
		} else {
			s.WriteString(fmt.Sprintf("\n  %d PRs", len(m.prs)))
		}
		if m.confirmAction == "approve" && m.confirmPR != nil {
			s.WriteString(lp.filter.Render(fmt.Sprintf("  Approve PR #%d? (y/n)", m.confirmPR.Number)))
		} else if m.refreshing {
			spinner := spinnerFrames[m.spinnerFrame]
			s.WriteString(lp.loading.Render("  " + spinner + " Refreshing"))
		}
	}

	s.WriteString("\n")
	s.WriteString(lp.help.Render(truncateToWidth("  ?: help · /: filter · v: view · enter: checkout · q: quit", rowWidth)))
	s.WriteString("\n")

	return s.String()
}

// listPainters holds the cached painters for one list frame.
type listPainters struct {
	dim, loading, filter, help               painter
	caret, selNormal, selAuthor, selReviewer painter
	selBranch, selAdds, selDels              painter
	normal, reviewer, branch, adds, dels     painter
	badge, selBadge                          [6]painter
}

func newListPainters() listPainters {
	lp := listPainters{
		dim:         sty("240", "", false, false),
		loading:     sty("214", "", false, false),
		filter:      sty("220", "", true, false),
		help:        sty("241", "", false, false),
		caret:       sty("33", "", true, false),
		selNormal:   sty("15", "", true, false),
		selAuthor:   sty("", "", true, false),
		selReviewer: sty("221", "", true, false),
		selBranch:   sty("183", "", true, false),
		selAdds:     sty("114", "", true, false),
		selDels:     sty("203", "", true, false),
		normal:      sty("252", "", false, false),
		reviewer:    sty("214", "", false, false),
		branch:      sty("141", "", false, false),
		adds:        sty("78", "", false, false),
		dels:        sty("196", "", false, false),
	}
	for k := range badgeLabels {
		lp.badge[k] = sty(badgeColors[k], "", false, false)
		lp.selBadge[k] = sty(badgeSelColors[k], "", true, false)
	}
	return lp
}

const spaceRun = "                                                                                "

func spaces(n int) string {
	if n <= len(spaceRun) {
		return spaceRun[:n]
	}
	return strings.Repeat(" ", n)
}

// isPlainASCII reports whether every byte is printable ASCII, where width == len.
func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func displayWidth(s string) int {
	if isPlainASCII(s) {
		return len(s)
	}
	return lipgloss.Width(s)
}

func truncate(s string, max int) string {
	if isPlainASCII(s) {
		if len(s) <= max {
			return s
		}
		if max > 3 {
			return s[:max-3] + "..."
		}
		return "..."
	}
	if displayWidth(s) <= max {
		return s
	}
	// Truncate rune by rune until we fit
	runes := []rune(s)
	for i := len(runes); i > 0; i-- {
		truncated := string(runes[:i]) + "..."
		if displayWidth(truncated) <= max {
			return truncated
		}
	}
	return "..."
}

// padW pads s to width and returns the resulting display width.
func padW(s string, width int) (string, int) {
	w := displayWidth(s)
	if w >= width {
		return s, w
	}
	return s + spaces(width-w), width
}

func padLeftW(s string, width int) (string, int) {
	w := displayWidth(s)
	if w >= width {
		return s, w
	}
	return spaces(width-w) + s, width
}

func pad(s string, width int) string {
	out, _ := padW(s, width)
	return out
}

func padLeft(s string, width int) string {
	out, _ := padLeftW(s, width)
	return out
}

func truncateToWidth(s string, max int) string {
	if displayWidth(s) <= max {
		return s
	}
	runes := []rune(s)
	for i := len(runes); i > 0; i-- {
		truncated := string(runes[:i])
		if displayWidth(truncated) <= max {
			return truncated
		}
	}
	return ""
}

func stripAnsi(s string) string {
	// Simple ANSI stripper for badge text
	var b strings.Builder
	b.Grow(len(s))
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func main() {
	// Parse flags
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--demo":
			demoMode = true
		case "--mine", "-m":
			mineMode = true
		}
	}

	// Load gh auth token once for direct GraphQL HTTP calls.
	if !demoMode {
		if err := loadGHToken(); err != nil {
			fmt.Fprintln(os.Stderr, "Error: failed to read gh auth token. Run: gh auth login")
			os.Exit(1)
		}
	}

	// In demo mode, skip org detection
	if demoMode {
		orgs = []string{"acme-corp"}
	} else if mineMode {
		if meta, ok := loadCachedMeta(); ok && meta.CurrentUser != "" {
			currentUser = meta.CurrentUser
		} else {
			currentUser = getCurrentUser()
			saveCachedMeta(metaCache{CurrentUser: currentUser})
		}
	} else if orgEnv := os.Getenv("SUP_ORG"); orgEnv != "" {
		orgs = strings.Split(orgEnv, ",")
		if meta, ok := loadCachedMeta(); ok && meta.CurrentUser != "" {
			currentUser = meta.CurrentUser
		} else {
			currentUser = getCurrentUser()
			saveCachedMeta(metaCache{Orgs: orgs, CurrentUser: currentUser})
		}
	} else if meta, ok := loadCachedMeta(); ok && len(meta.Orgs) > 0 {
		// Fast path: cached orgs + user. Background refresh runs after the TUI starts.
		orgs = meta.Orgs
		currentUser = meta.CurrentUser
	} else {
		// First run — block on org/user lookup so we have something to query.
		var err error
		orgs, err = fetchUserOrgs()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintln(os.Stderr, "Make sure you're logged in with: gh auth login")
			os.Exit(1)
		}
		if len(orgs) == 0 {
			fmt.Fprintln(os.Stderr, "No organizations found. Use --mine to see your PRs, or set SUP_ORG.")
			os.Exit(1)
		}
		currentUser = getCurrentUser()
		saveCachedMeta(metaCache{Orgs: orgs, CurrentUser: currentUser})
	}

	if !lipgloss.HasDarkBackground() {
		glamourStyle = "light"
	}

	go pruneDiskCache()
	go pruneImageCache()
	detectImageMode()

	p := tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	finalModel, err := p.Run()
	kittyFreeAll()
	if fm, ok := finalModel.(model); ok && fm.prsDirty {
		savePRsToCache(fm.prs)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	m := finalModel.(model)
	if demoMode {
		return
	}

	if m.selected != nil {
		pr := m.selected
		repoPath := findRepoPath(pr.Repository.Name)

		if repoPath == "" {
			fmt.Fprintf(os.Stderr, "Repo '%s' not found in common locations.\n", pr.Repository.Name)
			fmt.Fprintf(os.Stderr, "Clone it: gh repo clone %s/%s\n",
				pr.Repository.Owner.Login, pr.Repository.Name)
			fmt.Fprintf(os.Stderr, "Or set SUP_DEV_DIR to your repos directory.\n")
			os.Exit(1)
		}

		// Check if the branch is already checked out in a worktree
		targetPath := repoPath
		if wtPath := findWorktreePath(repoPath, pr.HeadRefName); wtPath != "" {
			fmt.Printf("Branch '%s' already checked out at %s\n", pr.HeadRefName, wtPath)
			targetPath = wtPath
		} else {
			// Change to repo directory and run gh pr checkout
			if err := os.Chdir(repoPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: could not cd to %s: %v\n", repoPath, err)
				os.Exit(1)
			}

			fmt.Printf("Checking out PR #%d in %s...\n", pr.Number, repoPath)
			cmd := exec.Command("gh", "pr", "checkout", fmt.Sprintf("%d", pr.Number), "--force")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: gh pr checkout failed: %v\n", err)
				os.Exit(1)
			}
		}

		// Write path for shell wrapper to cd into
		os.WriteFile(selectionFile, []byte(targetPath), 0644)
	} else {
		// No selection - clean up any stale selection file
		os.Remove(selectionFile)
	}
}
