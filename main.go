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
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

var (
	orgs        []string // Auto-detected or from SUP_ORG
	mineMode    bool     // Show PRs involving current user
	demoMode    bool     // Show mock data for screenshots
	currentUser string   // Authenticated GitHub username
	ghToken     string   // Cached `gh auth token` for direct HTTP calls
	startPR     *PR      // PR named on the command line, opened on launch

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
	req, err := http.NewRequest("POST", githubAPI+"/graphql", bytes.NewReader(body))
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
	return stripJSONControls(data), resp.StatusCode, nil
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
	if err := json.Unmarshal(stripJSONControls(data), &prs); err != nil {
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
	// Partial rows came from discovery and are still missing stats and review
	// state. It's persisted so a quit mid-refresh still refetches them.
	Partial   bool `json:"partial,omitempty"`
	IsDraft   bool `json:"isDraft"`
	Additions int  `json:"additions"`
	Deletions int  `json:"deletions"`
	Author    struct {
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
	actionPending     bool              // true while a review submission is in flight
	actionStatus      string            // transient success/error feedback for review actions
	helpMode          bool              // true while the help overlay is showing
	palette           *commandPalette   // non-nil while the command palette is open
	merge             *mergeDialog      // non-nil while the merge modal is open
	merging           map[string]int    // prKey -> number of merges in flight
	previewOn         bool              // split the list with a preview of the cursor's PR
	preview           *prDetailView     // the preview pane's view, kept separate from detail
	compactList       bool              // render the narrow list used beside the preview
	visibleCount      int               // for animation
	spinnerFrame      int               // for loading spinner
	refreshID         int               // increments each refresh; stale results are dropped
	discovered        map[string]prStub // open PRs found by the in-flight refresh
	pendingDiscover   int               // search shards still discovering
	discoverFailed    bool              // a shard failed, so don't prune
	pendingRows       int               // row batches still loading for the current refresh
	lastRefresh       time.Time         // when the list was last fully refreshed
	statusFilterIndex int               // current index in statusFilters array (-1 means no filter)
	authorFilter      string            // author filter (e.g., "!username"), empty means no author filter
	repos             []string          // repo names present in m.prs, sorted; rebuilt on every refresh
	repoFilterIndex   int               // current index into repos (-1 means no repo filter)
	detail            *prDetailView     // non-nil while the full PR view is open
	cache             map[string]*prCacheEntry
	hoverSeq          int // invalidates pending prefetch ticks when the cursor moves
	warmQueue         []PR
	warmInFlight      int
	prsDirty          bool // list changed since the last cache write
	prsSaveScheduled  bool
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
	body   string // kept so a rejected review's text isn't lost
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

// sortPRs puts ready PRs before drafts, most recently updated first.
func sortPRs(prs []PR) {
	sort.SliceStable(prs, func(i, j int) bool {
		a, b := prs[i], prs[j]
		if a.IsDraft != b.IsDraft {
			return !a.IsDraft
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return prKey(a) < prKey(b)
	})
}

func initialModel() model {
	// Skip cache in demo mode
	if !demoMode {
		cached := loadCachedPRs()
		if cached != nil {
			sortPRs(cached)
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
	for i := range prs {
		prs[i].HeadRefOid = fmt.Sprintf("%07x", prs[i].Number*7919)
		prs[i].UpdatedAt = demoNow.Add(-time.Duration(i*13) * time.Minute)
	}
	for i, d := range demoPRs {
		prs = append(prs, d.toPR(len(prs)+i))
	}
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
// findRepoPath finds a local clone of owner/name, checking its origin so a
// same-named repo from another org isn't picked.
func findRepoPath(owner, name string) string {
	home := os.Getenv("HOME")
	var candidates []string
	if devDir := os.Getenv("SUP_DEV_DIR"); devDir != "" {
		candidates = append(candidates, filepath.Join(devDir, name))
	}
	for _, dir := range defaultDevDirs {
		candidates = append(candidates, filepath.Join(home, dir, name))
	}
	for _, path := range candidates {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			continue
		}
		out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
		if err != nil {
			continue
		}
		if slug, err := slugFromRemote(strings.TrimSpace(string(out))); err == nil && strings.EqualFold(slug, owner+"/"+name) {
			return path
		}
	}
	return ""
}

// getCurrentUser gets the authenticated GitHub username
func getCurrentUser() string {
	login, _, _ := fetchViewer()
	return login
}

// fetchUserOrgs gets the list of organizations the user belongs to
func fetchUserOrgs() ([]string, error) {
	_, orgs, err := fetchViewer()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch orgs: %w", err)
	}
	return orgs, nil
}

func (m model) confirmPrompt() string {
	switch m.confirmAction {
	case "draft":
		return fmt.Sprintf("Convert PR #%d to draft?", m.confirmPR.Number)
	case "ready":
		return fmt.Sprintf("Mark PR #%d ready for review?", m.confirmPR.Number)
	}
	verb := map[string]string{"approve": "Approve", "close": "Close"}[m.confirmAction]
	return fmt.Sprintf("%s PR #%d?", verb, m.confirmPR.Number)
}

// confirmView is the centered modal asking to confirm m.confirmAction.
func (m model) confirmView() string {
	th := currentDiffTheme()
	pr := *m.confirmPR
	color, label := th.threadBorder, map[string]string{"draft": "Convert to draft", "ready": "Mark ready"}[m.confirmAction]
	switch m.confirmAction {
	case "approve":
		color, label = th.addBar, "Approve"
	case "close":
		color, label = th.delBar, "Close PR"
	}
	width := min(56, max(m.width-6, 30))
	muted := sty(th.muted, "", false, false)
	lines := []string{
		sty(color, "", true, false).Render(m.confirmPrompt()),
		"",
		sty(th.text, "", true, false).Render(truncate(pr.Title, width)),
		muted.Render(truncate(pr.Repository.Owner.Login+"/"+pr.Repository.Name+" · "+pr.Author.Login, width)),
		"",
		sty("16", color, true, false).Render(" y ") + " " + sty(th.text, "", false, false).Render(label) + "    " +
			sty(th.text, th.chipBg, false, false).Render(" esc ") + " " + muted.Render("Cancel"),
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(color)).Padding(1, 3).Width(width + 6)
	return box.Render(strings.Join(lines, "\n"))
}

func reviewDoneText(action string, pr PR) string {
	verb := map[string]string{"approve": "Approved", "request-changes": "Requested changes on", "comment": "Commented on"}[action]
	return fmt.Sprintf("✓ %s PR #%d", verb, pr.Number)
}

var reviewEvents = map[string]string{"approve": "APPROVE", "request-changes": "REQUEST_CHANGES", "comment": "COMMENT"}

func submitReviewCmd(action string, pr PR, body string) tea.Cmd {
	return func() tea.Msg {
		if demoMode {
			demoActions.Store(prKey(pr), action)
			return reviewSubmittedMsg{action: action, pr: pr}
		}
		review := map[string]string{"event": reviewEvents[action]}
		if body != "" {
			review["body"] = body
		}
		_, err := githubREST("POST", fmt.Sprintf("%s/pulls/%d/reviews", repoPath(pr), pr.Number), review)
		return reviewSubmittedMsg{action: action, pr: pr, body: body, err: err}
	}
}

// prRowFields are the PR fields a list row needs.
const prRowFields = `number title headRefName headRefOid updatedAt isDraft additions deletions
	author { login } reviewDecision
	reviewRequests(first: 5) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
	reviews(last: 5) { nodes { author { login } state } }`

const prRefreshBatch = 10

type prsRefreshedMsg struct {
	prs       []PR
	refreshID int // non-zero when part of a list refresh
	err       error
}

// fetchPRBatchCmd fetches full rows for several PRs in one GraphQL request using aliases.
func fetchPRBatchCmd(prs []PR, refreshID int) tea.Cmd {
	return func() tea.Msg {
		if demoMode {
			return prsRefreshedMsg{prs: demoRows(prs), refreshID: refreshID}
		}
		var q strings.Builder
		q.WriteString("{")
		for i, p := range prs {
			fmt.Fprintf(&q, "p%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { %s } }\n",
				i, p.Repository.Owner.Login, p.Repository.Name, p.Number, prRowFields)
		}
		q.WriteString("}")
		out, err := graphqlPOST(q.String())
		if err != nil {
			return prsRefreshedMsg{refreshID: refreshID, err: err}
		}
		var resp struct {
			Data map[string]*struct {
				PullRequest *PR `json:"pullRequest"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return prsRefreshedMsg{refreshID: refreshID, err: err}
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
		return prsRefreshedMsg{prs: updated, refreshID: refreshID}
	}
}

// demoRows returns the mock rows matching prs.
func demoRows(prs []PR) []PR {
	var out []PR
	for _, want := range prs {
		for _, mock := range mockPRs() {
			if samePR(mock, want) {
				out = append(out, applyDemoAction(mock))
			}
		}
	}
	return out
}

func fetchSinglePRCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		msg := fetchPRBatchCmd([]PR{pr}, 0)().(prsRefreshedMsg)
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
	newOrgs, newUser := orgs, currentUser
	if login, fetched, err := fetchViewer(); err == nil {
		if login != "" {
			newUser = login
		}
		if !mineMode && os.Getenv("SUP_ORG") == "" && len(fetched) > 0 {
			newOrgs = fetched
		}
	}
	saveCachedMeta(metaCache{Orgs: newOrgs, CurrentUser: newUser})
	return metaRefreshedMsg{orgs: newOrgs, currentUser: newUser}
}

type startRefreshMsg struct{}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{func() tea.Msg { return startRefreshMsg{} }, refreshMetaCmd, clockTick()}
	if startPR != nil {
		pr := *startPR
		cmds = append(cmds, func() tea.Msg { return openPRMsg{pr} })
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(kittyFlushedMsg); ok {
		handleKittyFlushed(f)
		return m, scheduleKittyFlush()
	}
	next, cmd := m.update(msg)
	nm := next.(model)
	if nm.detail != nil {
		// Panes render lazily, only once their tab is showing.
		nm.detail.ensure()
		nm.detail.clampScroll(nm.height)
	}
	nm.syncPreview()
	// Any rebuild may have queued image transmissions for the next frame.
	return nm, tea.Batch(cmd, scheduleKittyFlush())
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
		if m.preview != nil && m.preview.detail != nil {
			m.preview.rebuildForImages()
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

	case branchDeletedMsg:
		if msg.err != nil {
			m.actionStatus = "Error: " + msg.err.Error()
		}
		return m, nil

	case startRefreshMsg:
		return m, tea.Batch(m.startRefresh(), m.schedulePrefetch(), m.enqueueWarm())

	case warmDoneMsg:
		return m, m.handleWarmDone(msg)

	case discoveredMsg:
		return m, m.handleDiscovered(msg)

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

	case checkoutMsg:
		if pr, ok := m.currentPR(); ok {
			m.selected = &pr
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case paletteTickMsg:
		if p := m.palette; p != nil && p.frame < paletteFrames {
			p.frame++
			return m, paletteTick()
		}
		return m, nil

	case spinnerTickMsg:
		// Spin only while something is loading.
		if m.loading || m.refreshing || m.actionPending || (m.detail != nil && ((m.detail.loading && m.detail.detail == nil) || (m.detail.diffLoading && m.detail.diff == nil) ||
			(m.detail.picker != nil && (m.detail.picker.loading || m.detail.picker.saving)))) ||
			(m.merge != nil && m.mergeDetail() == nil) || len(m.merging) > 0 {
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
		m.actionStatus = reviewDoneText(msg.action, msg.pr)
		return m, submitReviewCmd(msg.action, msg.pr, msg.body)

	case reviewSubmittedMsg:
		if msg.err != nil {
			m.actionStatus = "Error: " + msg.err.Error()
			if msg.body != "" {
				path := filepath.Join(cacheDir(), "last-review.md")
				if os.WriteFile(path, []byte(msg.body), 0600) == nil {
					m.actionStatus += " · your text is saved in " + path
				}
			}
			return m, nil
		}
		// Refresh just this PR so its badge reflects the new review state.
		cmds := []tea.Cmd{fetchSinglePRCmd(msg.pr)}
		if (m.detail != nil && prKey(m.detail.pr) == prKey(msg.pr)) || (m.preview != nil && prKey(m.preview.pr) == prKey(msg.pr)) {
			cmds = append(cmds, fetchPRDetailCmd(msg.pr))
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

	case openPRMsg:
		for _, pr := range m.prs {
			if samePR(pr, msg.pr) {
				return m, m.openDetail(pr)
			}
		}
		// Not in the list yet: open the stub and fill in its row.
		return m, tea.Batch(m.openDetail(msg.pr), fetchSinglePRCmd(msg.pr))

	case prRefreshedMsg:
		if msg.err != nil {
			// Best-effort; leave the list alone on failure.
			return m, nil
		}
		return m, m.applyRefreshedPRs([]PR{msg.pr})

	case prsRefreshedMsg:
		return m, m.handleRowsRefreshed(msg)

	case autoRefreshMsg:
		// Only if nothing refreshed since this timer was set.
		if msg.refreshID == m.refreshID && !m.refreshing {
			m.refreshing = true
			return m, m.startRefresh()
		}
		return m, nil

	case tea.FocusMsg:
		if !m.refreshing && time.Since(m.lastRefresh) > focusRefreshAfter {
			m.refreshing = true
			return m, m.startRefresh()
		}
		return m, nil

	case clockTickMsg:
		// Keeps "updated Xm ago" honest while idle.
		return m, clockTick()

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		// ctrl+c always quits, even from the help overlay.
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.palette != nil {
			return m.handlePaletteInput(msg)
		}
		if m.merge != nil {
			return m.handleMergeInput(msg)
		}
		if msg.String() == "ctrl+k" && !m.textInputActive() {
			m.helpMode = false
			m.palette = &commandPalette{}
			return m, paletteTick()
		}
		// In help overlay, only ?/esc/q dismiss; everything else is ignored.
		if m.helpMode {
			switch msg.String() {
			case "?", "esc", "q":
				m.helpMode = false
			}
			return m, nil
		}
		if m.detail != nil {
			return m.handleDetailInput(msg)
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
		case "ctrl+u": // cmd+backspace in most macOS terminals
			m.filterText = ""
			m.applyFilter()
			return m, nil
		case "backspace", "ctrl+h", "del":
			if len(m.filterText) > 0 {
				m.filterText = m.filterText[:len(m.filterText)-1]
				m.applyFilter()
			}
			return m, nil
		}
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
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
// applyRefreshedPRs swaps updated rows in (adding new PRs), re-filtering once per batch.
func (m *model) applyRefreshedPRs(updated []PR) tea.Cmd {
	added := false
	for _, u := range updated {
		found := false
		for i := range m.prs {
			if samePR(m.prs[i], u) {
				m.prs[i] = u
				found = true
				break
			}
		}
		if !found {
			m.prs = append(m.prs, u)
			added = true
		}
		if m.detail != nil && samePR(m.detail.pr, u) {
			m.detail.pr = u
		}
	}
	sortPRs(m.prs) // updatedAt may have moved
	if added {
		m.syncRepoList()
	}
	// Every active filter (repo, status, author) must survive the rebuild, not just typed text.
	m.applyFilterKeepingCursor()
	m.visibleCount = len(m.filtered)
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
		case "y", "Y", "enter":
			action := m.confirmAction
			pr := *m.confirmPR
			m.confirmAction = ""
			m.confirmPR = nil
			switch action {
			case "close":
				return m, runCloseCmd(pr)
			case "draft", "ready":
				return m, runDraftCmd(pr, action == "draft")
			}
			// Optimistic: confirm now; an error replaces this if GitHub rejects it.
			m.actionStatus = reviewDoneText(action, pr)
			return m, submitReviewCmd(action, pr, "")
		case "n", "N", "esc", "q":
			m.confirmAction = ""
			m.confirmPR = nil
		}
		return m, nil
	}

	// Filter cycling keys run before the transient-feedback reset below.
	switch msg.String() {
	case "s":
		m.cycleStatusFilter(1)
		return m, nil
	case "S":
		m.cycleStatusFilter(-1)
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

	case " ":
		m.previewOn = !m.previewOn
		return m, m.schedulePrefetch()

	case "ctrl+d", "ctrl+u":
		if v := m.preview; v != nil && m.previewActive() {
			step := v.bodyHeight(m.height) / 2
			if msg.String() == "ctrl+u" {
				step = -step
			}
			v.panes[tabConversation].scroll += step
			v.clampScroll(m.height)
		}
		return m, nil

	case "enter", "v":
		if pr, ok := m.currentPR(); ok {
			return m, m.openDetail(pr)
		}
		return m, nil

	case "m":
		if pr, ok := m.currentPR(); ok {
			return m, m.openMerge(pr)
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

	case "X":
		if pr, ok := m.currentPR(); ok {
			m.confirmAction = "close"
			m.confirmPR = &pr
		}
		return m, nil

	case "W":
		if pr, ok := m.currentPR(); ok {
			m.confirmAction = "draft"
			if pr.IsDraft {
				m.confirmAction = "ready"
			}
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

var statusFilters = []string{"approved", "denied", "review", "commented", "open", "draft"}

func (m *model) cycleStatusFilter(delta int) {
	n := len(statusFilters) + 1 // last position is "no filter"
	pos := m.statusFilterIndex
	if pos < 0 {
		pos = len(statusFilters)
	}
	pos = ((pos+delta)%n + n) % n
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
	badgeLabels    = [7]string{"[Draft]", "[Approved]", "[Denied]", "[Commented]", "[Review]", "[Open]", "[…]"}
	badgeColors    = [7]string{"241", "78", "196", "117", "214", "39", "238"}
	badgeSelColors = [7]string{"250", "114", "203", "159", "221", "81", "244"}
)

// statusPending is a row discovered but not yet loaded; its review state is unknown.
const statusPending = 6

func statusKind(pr PR) int {
	if pr.Partial {
		return statusPending
	}
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

// banner shows a prompt or action result as a colored pill so it stands out.
func banner(text string) string {
	fg, bg := "16", "35" // success
	switch {
	case strings.HasPrefix(text, "Error"), strings.Contains(text, "cancelled"):
		fg, bg = "15", "160"
	}
	return sty(fg, bg, true, false).Render(" " + text + " ")
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

type helpSection struct {
	title string
	items [][2]string // key, description
}

// helpSections lists the keys for the current screen; the help modal and command palette both read it.
func (m model) helpSections() []helpSection {
	sections := []helpSection{
		{"Navigation", [][2]string{
			{"j / ↓", "Move down"},
			{"k / ↑", "Move up"},
			{"g / G", "Top / bottom"},
			{"space", "Toggle preview pane"},
			{"ctrl+d / u", "Scroll preview"},
		}},
		{"Filter", [][2]string{
			{"/", "Open filter"},
			{"s / S", "Cycle status (fwd / back)"},
			{"p / P", "Cycle repo (fwd / back)"},
			{"#repo", "Filter by repo"},
			{"@user", "Filter by reviewer"},
			{"!user", "Filter by author"},
			{"a", "My PRs"},
			{"r", "My reviews"},
		}},
		{"Actions", [][2]string{
			{"Enter", "Open PR view"},
			{"m", "Merge PR"},
			{"d", "Open the PR's diff"},
			{"A", "Approve"},
			{"D", "Request changes"},
			{"M", "Comment"},
			{"o", "Open in browser"},
			{"O", "Open all needing review"},
			{"c", "Copy PR link"},
			{"W", "Convert to draft / mark ready"},
			{"X", "Close PR"},
		}},
		{"Other", [][2]string{
			{"R", "Refresh PR list"},
			{"?", "Toggle this help"},
			{"ctrl+k", "Command palette"},
			{"esc", "Clear filter (or quit)"},
			{"q", "Quit"},
		}},
	}
	if m.detail != nil {
		sections = []helpSection{
			{"Navigation", [][2]string{
				{"j / k", "Scroll down / up"},
				{"ctrl+d / u", "Half page down / up"},
				{"space / ctrl+b", "Page down / up"},
				{"g / G", "Top / bottom"},
				{"tab / S-tab", "Next / previous tab"},
				{"1-4", "Jump to tab"},
				{"[ / ]", "Previous / next file or item"},
				{"n / N", "Next / prev comment or match"},
				{"/", "Search"},
			}},
			{"Files tab", [][2]string{
				{"d", "Go to the Files tab"},
				{"s", "Cycle layout (auto / split / unified)"},
				{"w", "Toggle line wrap"},
				{"x", "Collapse / expand file"},
			}},
			{"Actions", [][2]string{
				{"m", "Merge PR"},
				{"r", "Edit reviewers"},
				{"A", "Approve"},
				{"D", "Request changes"},
				{"M", "Comment"},
				{"o", "Open in browser"},
				{"c", "Copy PR link"},
				{"W", "Convert to draft / mark ready"},
				{"X", "Close PR"},
				{"R", "Refresh PR"},
			}},
			{"Other", [][2]string{
				{"?", "Toggle this help"},
				{"ctrl+k", "Command palette"},
				{"esc / q", "Clear search, or back to list"},
				{"ctrl+c", "Quit"},
			}},
		}
	}
	return sections
}

func (m model) helpView() string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	sections := m.helpSections()

	column := func(secs []helpSection) string {
		keyCol := 0
		for _, sec := range secs {
			for _, item := range sec.items {
				keyCol = max(keyCol, lipgloss.Width(item[0])+2)
			}
		}
		var c []string
		for i, sec := range secs {
			if i > 0 {
				c = append(c, "")
			}
			c = append(c, sectionStyle.Render(sec.title))
			for _, item := range sec.items {
				c = append(c, keyStyle.Render(pad(item[0], keyCol))+descStyle.Render(item[1]))
			}
		}
		return strings.Join(c, "\n")
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, column(sections[:2]), "    ", column(sections[2:]))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("141")).Padding(1, 2)
	render := func(body string) string {
		return box.Render(headerStyle.Render("Keybindings") + "\n\n" + body + "\n\n" + dimStyle.Render("? · esc · q to close"))
	}
	if out := render(body); lipgloss.Width(out) <= m.width || m.width == 0 {
		return out
	}
	return render(column(sections)) // too narrow for two columns
}

// overlayCenter draws fg centered over bg, which is dimmed to plain text behind it.
func overlayCenter(bg, fg string, width, height int) string {
	return overlayAt(bg, fg, width, height, max((height-lipgloss.Height(fg))/2, 0))
}

// overlayAt draws fg horizontally centered at row y over a dimmed bg.
func overlayAt(bg, fg string, width, height, y int) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}
	fgLines := strings.Split(fg, "\n")
	fw := lipgloss.Width(fg)
	x := max((width-fw)/2, 0)
	for i, line := range bgLines {
		plain := ansi.Strip(line)
		if strings.ContainsRune(plain, '\U0010EEEE') {
			plain = "" // kitty image placeholders mean nothing without their colors
		}
		if j := i - y; j >= 0 && j < len(fgLines) {
			left := pad(runewidth.Truncate(plain, x, ""), x)
			right := runewidth.TruncateLeft(plain, x+fw, "")
			bgLines[i] = dim.Render(left) + fgLines[j] + dim.Render(right)
		} else {
			bgLines[i] = dim.Render(plain)
		}
	}
	return strings.Join(bgLines, "\n")
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
	if m.merge != nil {
		under := m // render the screen underneath without the modal
		under.merge = nil
		return overlayCenter(under.view(), m.mergeView(), m.width, m.height)
	}
	if m.confirmPR != nil {
		under := m // render the screen underneath without the modal
		under.confirmAction, under.confirmPR = "", nil
		return overlayCenter(under.view(), m.confirmView(), m.width, m.height)
	}
	if m.palette != nil {
		under := m // render the screen underneath without the palette
		under.palette = nil
		return overlayAt(under.view(), m.paletteView(), m.width, m.height, paletteTop(m.height))
	}
	if m.helpMode {
		m.helpMode = false // m is a copy; render the screen underneath
		return overlayCenter(m.view(), m.helpView(), m.width, m.height)
	}
	if m.detail != nil {
		return m.detailView()
	}
	if m.previewActive() {
		return m.splitView()
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
	if separatorWidth < 60 && !m.compactList {
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
	case filterLine == "  ":
		s.WriteString(filterLine)
	default:
		s.WriteString(lp.filter.Render(filterLine))
	}
	s.WriteString("\n")

	// Always show header
	if m.compactList {
		s.WriteString(lp.dim.Render("  " + pad("STATUS", colStatus) + pad("#", colNum) + "TITLE"))
	} else {
		s.WriteString(lp.dim.Render("  " + pad("STATUS", colStatus) + pad("REPO", colRepo) + pad("#", colNum) + pad("TITLE", colTitle) + pad("AUTHOR", colAuthor) + pad("REVIEWER", colReviewer) + pad("BRANCH", colBranch) + "+/-"))
	}
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
			if m.compactList {
				s.WriteString(compactRow(pr, isSelected, rowWidth+2, lp) + "\n")
				continue
			}

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
			addsText, delsText := "+"+strconv.Itoa(pr.Additions), "-"+strconv.Itoa(pr.Deletions)
			if pr.Partial {
				addsText, delsText = "", ""
			}
			addsPadded, addsW := padLeftW(addsText, leftDiff)
			delsPadded, delsW := padLeftW(delsText, rightDiff)
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
		if t := m.mergingText(); t != "" {
			s.WriteString(lp.loading.Render("  " + t))
		}
		if m.actionPending {
			s.WriteString(lp.loading.Render("  " + spinnerFrames[m.spinnerFrame] + " Submitting review..."))
		} else if m.actionStatus != "" {
			s.WriteString("  " + banner(m.actionStatus))
		} else if m.refreshing {
			s.WriteString(lp.loading.Render("  " + spinnerFrames[m.spinnerFrame] + " Refreshing"))
		} else if status := m.freshness(); status != "" {
			s.WriteString(lp.dim.Render("  " + status))
		}
	}

	s.WriteString("\n")
	hints := "  ctrl+k: commands · /: filter · space: preview · enter: open · ?: help"
	if m.compactList {
		hints = "  space: close · ctrl+d/u: scroll · v: open"
	}
	s.WriteString(lp.help.Render(truncateToWidth(hints, rowWidth)))
	s.WriteString("\n")

	return s.String()
}

// listPainters holds the cached painters for one list frame.
type listPainters struct {
	dim, loading, filter, help               painter
	caret, selNormal, selAuthor, selReviewer painter
	selBranch, selAdds, selDels              painter
	normal, reviewer, branch, adds, dels     painter
	badge, selBadge                          [7]painter
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
	var prArgs []string
	for i, arg := range os.Args[1:] {
		switch arg {
		case "--demo":
			demoMode = true
		case "--mine", "-m":
			mineMode = true
		case "--complete":
			repo := ""
			if i+2 < len(os.Args) {
				repo = os.Args[i+2]
			}
			meta, _ := loadCachedMeta()
			for _, line := range completions(repo, loadCachedPRs(), meta.Orgs) {
				fmt.Println(line)
			}
			return
		case "--zsh-completion":
			fmt.Print(zshCompletion)
			return
		default:
			prArgs = append(prArgs, arg)
		}
	}
	if len(prArgs) > 0 {
		meta, _ := loadCachedMeta()
		pr, err := parsePRArgs(prArgs, loadCachedPRs(), meta.Orgs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		startPR = &pr
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
		orgs, currentUser = []string{"acme-corp"}, "sarah"
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
	enableGraphemeWidths()

	p := tea.NewProgram(initialModel(), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	finalModel, err := p.Run()
	kittyFreeAll()
	restoreGraphemeWidths()
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
		slug := pr.Repository.Owner.Login + "/" + pr.Repository.Name
		repoPath := findRepoPath(pr.Repository.Owner.Login, pr.Repository.Name)

		if repoPath == "" {
			fmt.Fprintf(os.Stderr, "No clone of %s found in common locations.\n", slug)
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
			if err := os.Chdir(repoPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: could not cd to %s: %v\n", repoPath, err)
				os.Exit(1)
			}

			fmt.Printf("Checking out PR #%d in %s...\n", pr.Number, repoPath)
			// No --force: it would reset a local branch and drop unpushed commits.
			cmd := exec.Command("gh", "pr", "checkout", fmt.Sprintf("%d", pr.Number), "--repo", slug)
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
