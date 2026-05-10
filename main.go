package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
)

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

func savePRsToCache(prs []PR) {
	data, err := json.Marshal(prs)
	if err != nil {
		return
	}
	os.WriteFile(getCacheFilePath(), data, 0644)
}

type PR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	IsDraft     bool   `json:"isDraft"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
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
	prs          []PR
	filtered     []PR
	cursor       int
	selected     *PR
	filterMode   bool
	filterText   string
	err          error
	quitting     bool
	width        int
	height       int
	loading      bool
	refreshing   bool // true when fetching new data while showing cached data
	loadingDiff   bool // true while fetching diff before launching hunk
	diffError     string
	confirmAction string // non-empty while awaiting y/n confirmation (e.g. "approve")
	confirmPR     *PR
	actionPending bool   // true while a review submission is in flight
	actionStatus  string // transient success/error feedback for review actions
	helpMode      bool   // true while the help overlay is showing
	visibleCount  int    // for animation
	spinnerFrame  int    // for loading spinner
}

type prsLoadedMsg struct {
	prs []PR
	err error
}

type diffFetchedMsg struct {
	patch []byte
	mode  string // "split" or "stack" — passed through to hunk via --mode
	err   error
}

type hunkDoneMsg struct {
	err error
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
				prs:          cached,
				filtered:     cached,
				cursor:       0,
				loading:      false,
				refreshing:   true,
				visibleCount: len(cached),
			}
		}
	}
	return model{
		prs:          []PR{},
		filtered:     []PR{},
		cursor:       0,
		loading:      true,
		visibleCount: 0,
	}
}

type graphQLResponse struct {
	Data struct {
		Search struct {
			Nodes []PR `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
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

func fetchPRs() tea.Msg {
	if demoMode {
		return prsLoadedMsg{prs: mockPRs()}
	}

	// Build the search query
	var searchQuery string
	if mineMode {
		searchQuery = "involves:@me is:pr is:open"
	} else {
		// Build org query: org:foo org:bar ...
		var orgParts []string
		for _, org := range orgs {
			orgParts = append(orgParts, "org:"+org)
		}
		searchQuery = strings.Join(orgParts, " ") + " is:pr is:open"
	}

	query := fmt.Sprintf(`{
		search(query: "%s", type: ISSUE, first: 100) {
			nodes {
				... on PullRequest {
					number
					title
					headRefName
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
	}`, searchQuery)

	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	output, err := cmd.Output()
	if err != nil {
		return prsLoadedMsg{err: fmt.Errorf("failed to fetch PRs: %w", err)}
	}

	var resp graphQLResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return prsLoadedMsg{err: fmt.Errorf("failed to parse PRs: %w", err)}
	}

	return prsLoadedMsg{prs: resp.Data.Search.Nodes}
}

func fetchDiffCmd(pr PR, mode string) tea.Cmd {
	return func() tea.Msg {
		repoSlug := fmt.Sprintf("%s/%s", pr.Repository.Owner.Login, pr.Repository.Name)
		cmd := exec.Command("gh", "pr", "diff", fmt.Sprintf("%d", pr.Number), "--repo", repoSlug)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return diffFetchedMsg{mode: mode, err: fmt.Errorf("%s", msg)}
		}
		return diffFetchedMsg{patch: out, mode: mode}
	}
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

func fetchSinglePRCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		query := fmt.Sprintf(`{
			repository(owner: "%s", name: "%s") {
				pullRequest(number: %d) {
					number
					title
					headRefName
					isDraft
					additions
					deletions
					author { login }
					reviewDecision
					reviewRequests(first: 5) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
					reviews(last: 5) { nodes { author { login } state } }
				}
			}
		}`, pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)

		cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
		out, err := cmd.Output()
		if err != nil {
			return prRefreshedMsg{err: err}
		}
		var resp struct {
			Data struct {
				Repository struct {
					PullRequest PR `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return prRefreshedMsg{err: err}
		}
		// GraphQL response doesn't include repository under pullRequest, restore it.
		updated := resp.Data.Repository.PullRequest
		updated.Repository = pr.Repository
		return prRefreshedMsg{pr: updated}
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

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchPRs, refreshMetaCmd, spinnerTick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case prsLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			m.refreshing = false
			return m, nil
		}

		// Sort PRs oldest first
		sortPRsByOldestFirst(msg.prs)

		// Save to cache (skip in demo mode)
		if !demoMode {
			savePRsToCache(msg.prs)
		}

		// Preserve selection by finding the same PR in the new list
		var selectedPRNumber int
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			selectedPRNumber = m.filtered[m.cursor].Number
		}

		m.prs = msg.prs

		// Re-apply filter if active
		if m.filterText != "" {
			m.applyFilter()
		} else {
			m.filtered = msg.prs
		}

		// Restore cursor position to the same PR if it still exists
		if selectedPRNumber > 0 {
			for i, pr := range m.filtered {
				if pr.Number == selectedPRNumber {
					m.cursor = i
					break
				}
			}
		}

		// Clamp cursor to valid range
		if m.cursor >= len(m.filtered) {
			m.cursor = len(m.filtered) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}

		wasRefreshing := m.refreshing
		m.loading = false
		m.refreshing = false

		// Only animate if we weren't showing cached data
		if !wasRefreshing {
			m.visibleCount = 0
			return m, tick()
		}
		m.visibleCount = len(m.filtered)
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
		if m.loading || m.refreshing || m.loadingDiff || m.actionPending {
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
		return m, fetchSinglePRCmd(msg.pr)

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
		for i := range m.prs {
			if m.prs[i].Number == msg.pr.Number &&
				m.prs[i].Repository.Name == msg.pr.Repository.Name &&
				m.prs[i].Repository.Owner.Login == msg.pr.Repository.Owner.Login {
				m.prs[i] = msg.pr
				break
			}
		}
		// Preserve cursor across the filter rebuild.
		var selectedPRNumber int
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			selectedPRNumber = m.filtered[m.cursor].Number
		}
		if m.filterText != "" {
			m.applyFilter()
		} else {
			m.filtered = m.prs
		}
		if selectedPRNumber > 0 {
			for i, pr := range m.filtered {
				if pr.Number == selectedPRNumber {
					m.cursor = i
					break
				}
			}
		}
		if m.cursor >= len(m.filtered) {
			m.cursor = len(m.filtered) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		if !demoMode {
			savePRsToCache(m.prs)
		}
		return m, nil

	case diffFetchedMsg:
		m.loadingDiff = false
		if msg.err != nil {
			m.diffError = msg.err.Error()
			return m, nil
		}
		args := []string{"patch"}
		if msg.mode != "" {
			args = append(args, "--mode", msg.mode)
		}
		args = append(args, "-")
		hunk := exec.Command("hunk", args...)
		hunk.Stdin = bytes.NewReader(msg.patch)
		return m, tea.ExecProcess(hunk, func(err error) tea.Msg {
			return hunkDoneMsg{err: err}
		})

	case hunkDoneMsg:
		if msg.err != nil {
			m.diffError = fmt.Sprintf("hunk error: %v", msg.err)
		}
		return m, nil

	case tea.KeyMsg:
		// ctrl+c always quits, even from the help overlay.
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
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
			if m.filterText != "" {
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

func (m *model) applyFilter() {
	if m.filterText == "" {
		m.filtered = m.prs
		m.cursor = 0
		return
	}

	filter := strings.ToLower(m.filterText)
	m.filtered = nil

	// @username prefix: match requested reviewers only
	if strings.HasPrefix(filter, "@") {
		userFilter := strings.TrimPrefix(filter, "@")
		for _, pr := range m.prs {
			requested := strings.ToLower(getRequestedReviewerNames(pr))
			if requested != "" && strings.Contains(requested, userFilter) {
				m.filtered = append(m.filtered, pr)
			}
		}
		m.cursor = 0
		return
	}

	// !username prefix: match author only
	if strings.HasPrefix(filter, "!") {
		userFilter := strings.TrimPrefix(filter, "!")
		for _, pr := range m.prs {
			if strings.Contains(strings.ToLower(pr.Author.Login), userFilter) {
				m.filtered = append(m.filtered, pr)
			}
		}
		m.cursor = 0
		return
	}

	// Default: search all fields including reviewer
	for _, pr := range m.prs {
		statusLabel := statusLabelForFilter(pr)
		reviewers := getAllReviewerNames(pr)
		searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s %s %s #%d %d %s",
			pr.Repository.Name, pr.Title, pr.Author.Login, pr.HeadRefName, statusLabel,
			pr.Repository.Owner.Login, pr.Number, pr.Number, reviewers))
		if strings.Contains(searchText, filter) {
			m.filtered = append(m.filtered, pr)
		}
	}
	m.cursor = 0
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

	// Any key dismisses transient feedback from a previous action.
	m.diffError = ""
	m.actionStatus = ""

	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, nil

	case "g":
		m.cursor = 0
		return m, nil

	case "G":
		if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
		return m, nil

	case "a":
		if currentUser != "" {
			m.filterText = "!" + currentUser
			m.applyFilter()
		}
		return m, nil

	case "r":
		if currentUser != "" {
			m.filterText = "@" + currentUser
			m.applyFilter()
		}
		return m, nil

	case "R":
		m.refreshing = true
		return m, tea.Batch(fetchPRs, spinnerTick())

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

	case "d":
		if m.loadingDiff {
			return m, nil
		}
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			if _, err := exec.LookPath("hunk"); err != nil {
				m.diffError = "hunk not installed — npm i -g hunkdiff"
				return m, nil
			}
			m.diffError = ""
			m.loadingDiff = true
			return m, tea.Batch(fetchDiffCmd(m.filtered[m.cursor], "split"), spinnerTick())
		}
		return m, nil

	case "A":
		if m.actionPending || m.loadingDiff {
			return m, nil
		}
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			pr := m.filtered[m.cursor]
			m.confirmAction = "approve"
			m.confirmPR = &pr
		}
		return m, nil

	case "D":
		if m.actionPending || m.loadingDiff {
			return m, nil
		}
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			cmd, err := startEditorCmd("request-changes", m.filtered[m.cursor])
			if err != nil {
				m.actionStatus = "Error: " + err.Error()
				return m, nil
			}
			return m, cmd
		}
		return m, nil

	case "C":
		if m.actionPending || m.loadingDiff {
			return m, nil
		}
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			cmd, err := startEditorCmd("comment", m.filtered[m.cursor])
			if err != nil {
				m.actionStatus = "Error: " + err.Error()
				return m, nil
			}
			return m, cmd
		}
		return m, nil

	case "o":
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			pr := m.filtered[m.cursor]
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
			exec.Command("open", "-g", url).Start()
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

func getStatusBadge(pr PR) string {
	if pr.IsDraft {
		return draftStyle.Render("[Draft]")
	}

	switch pr.ReviewDecision {
	case "APPROVED":
		return approvedStyle.Render("[Approved]")
	case "CHANGES_REQUESTED":
		return changesRequestedStyle.Render("[Denied]")
	case "REVIEW_REQUIRED":
		// Check if last review was a comment
		if len(pr.Reviews.Nodes) > 0 && pr.Reviews.Nodes[0].State == "COMMENTED" {
			return commentedStyle.Render("[Commented]")
		}
		return reviewRequestedStyle.Render("[Review]")
	default:
		return openStyle.Render("[Open]")
	}
}

func getSelectedStatusBadge(pr PR) string {
	if pr.IsDraft {
		return selectedDraftStyle.Render("[Draft]")
	}

	switch pr.ReviewDecision {
	case "APPROVED":
		return selectedApprovedStyle.Render("[Approved]")
	case "CHANGES_REQUESTED":
		return selectedChangesRequestedStyle.Render("[Denied]")
	case "REVIEW_REQUIRED":
		if len(pr.Reviews.Nodes) > 0 && pr.Reviews.Nodes[0].State == "COMMENTED" {
			return selectedCommentedStyle.Render("[Commented]")
		}
		return selectedReviewRequestedStyle.Render("[Review]")
	default:
		return selectedOpenStyle.Render("[Open]")
	}
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
			{"@user", "Filter by reviewer"},
			{"!user", "Filter by author"},
			{"a", "My PRs"},
			{"r", "My reviews"},
		}},
		{"Actions", [][2]string{
			{"Enter", "Checkout PR"},
			{"d", "Review diff (hunk: 1=split · 2=stack · 0=auto)"},
			{"A", "Approve"},
			{"D", "Request changes"},
			{"C", "Comment"},
			{"o", "Open in browser"},
			{"O", "Open all needing review"},
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

func (m model) View() string {
	if m.helpMode {
		return m.helpView()
	}
	var s strings.Builder
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

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
	} else if m.filterText != "" {
		filterLine = fmt.Sprintf("  Filter: %s", m.filterText)
	}
	switch {
	case m.actionPending:
		spinner := spinnerFrames[m.spinnerFrame]
		s.WriteString(loadingStyle.Render("  " + spinner + " Submitting review..."))
	case m.actionStatus != "":
		style := approvedStyle
		if strings.HasPrefix(m.actionStatus, "Error") || strings.Contains(m.actionStatus, "cancelled") {
			style = changesRequestedStyle
		}
		s.WriteString(style.Render("  " + m.actionStatus))
	case m.diffError != "":
		s.WriteString(changesRequestedStyle.Render("  " + m.diffError))
	case filterLine == "  ":
		s.WriteString(filterLine)
	default:
		s.WriteString(filterStyle.Render(filterLine))
	}
	s.WriteString("\n")

	// Always show header
	s.WriteString(dimStyle.Render("  " + pad("STATUS", colStatus) + pad("REPO", colRepo) + pad("#", colNum) + pad("TITLE", colTitle) + pad("AUTHOR", colAuthor) + pad("REVIEWER", colReviewer) + pad("BRANCH", colBranch) + "+/-"))
	s.WriteString("\n")
	s.WriteString(dimStyle.Render("  " + strings.Repeat("─", separatorWidth)))
	s.WriteString("\n")

	if m.err != nil {
		s.WriteString(fmt.Sprintf("\n  Error: %v\n", m.err))
		s.WriteString(helpStyle.Render("\n  Press q to quit.\n"))
		return s.String()
	}

	if m.loading {
		spinner := spinnerFrames[m.spinnerFrame]
		s.WriteString(loadingStyle.Render("  " + spinner + " Loading..."))
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

			// Prepare padded values
			statusPlain := pad(stripAnsi(getStatusBadge(pr)), colStatus)
			repo := pad(truncate(pr.Repository.Name, colRepo-1), colRepo)
			num := pad(fmt.Sprintf("#%d", pr.Number), colNum)
			title := pad(truncate(pr.Title, colTitle-1), colTitle)
			author := pad(truncate(pr.Author.Login, colAuthor-1), colAuthor)
			reviewer := pad(truncate(getReviewer(pr), colReviewer-1), colReviewer)
			branch := pad(truncate(pr.HeadRefName, colBranch-1), colBranch)
			leftDiff := (colDiff - 1) / 2
			rightDiff := colDiff - 1 - leftDiff
			addsPlain := fmt.Sprintf("+%d", pr.Additions)
			delsPlain := fmt.Sprintf("-%d", pr.Deletions)
			addsPadded := padLeft(addsPlain, leftDiff)
			delsPadded := padLeft(delsPlain, rightDiff)
			diffPlain := addsPadded + " " + delsPadded
			rowPlain := cursor + statusPlain + repo + num + title + author + reviewer + branch + diffPlain

			if isSelected {
				s.WriteString(caretStyle.Render(cursor))
				s.WriteString(getSelectedStatusBadge(pr) + strings.Repeat(" ", colStatus-len(stripAnsi(getSelectedStatusBadge(pr)))))
				s.WriteString(selectedNormalStyle.Render(repo))
				s.WriteString(selectedNormalStyle.Render(num))
				s.WriteString(selectedNormalStyle.Render(title))
				s.WriteString(selectedStyle.Render(author))
				s.WriteString(selectedReviewRequestedStyle.Render(reviewer))
				s.WriteString(selectedBranchStyle.Render(branch))
				s.WriteString(selectedAdditionsStyle.Render(addsPadded))
				s.WriteString(" ")
				s.WriteString(selectedDeletionsStyle.Render(delsPadded))

				currentWidth := displayWidth(rowPlain)
				if currentWidth < rowWidth {
					s.WriteString(strings.Repeat(" ", rowWidth-currentWidth))
				}
			} else {
				s.WriteString(cursor)
				// Apply colors after padding
				s.WriteString(getStatusBadge(pr) + strings.Repeat(" ", colStatus-len(stripAnsi(getStatusBadge(pr)))))
				s.WriteString(normalStyle.Render(repo))
				s.WriteString(normalStyle.Render(num))
				s.WriteString(normalStyle.Render(title))
				s.WriteString(dimStyle.Render(author))
				s.WriteString(reviewRequestedStyle.Render(reviewer))
				s.WriteString(branchStyle.Render(branch))
				s.WriteString(additionsStyle.Render(addsPadded))
				s.WriteString(" ")
				s.WriteString(deletionsStyle.Render(delsPadded))

				currentWidth := displayWidth(rowPlain)
				if currentWidth < rowWidth {
					s.WriteString(strings.Repeat(" ", rowWidth-currentWidth))
				}
			}
			s.WriteString("\n")
		}

		if m.filterText != "" {
			s.WriteString(fmt.Sprintf("\n  %d of %d PRs", len(m.filtered), len(m.prs)))
		} else {
			s.WriteString(fmt.Sprintf("\n  %d PRs", len(m.prs)))
		}
		if m.confirmAction == "approve" && m.confirmPR != nil {
			s.WriteString(filterStyle.Render(fmt.Sprintf("  Approve PR #%d? (y/n)", m.confirmPR.Number)))
		} else if m.refreshing {
			spinner := spinnerFrames[m.spinnerFrame]
			s.WriteString(loadingStyle.Render("  " + spinner + " Refreshing"))
		} else if m.loadingDiff {
			spinner := spinnerFrames[m.spinnerFrame]
			s.WriteString(loadingStyle.Render("  " + spinner + " Loading diff..."))
		}
	}

	s.WriteString("\n")
	s.WriteString(helpStyle.Render(truncateToWidth("  ?: help · /: filter · enter: checkout · q: quit", rowWidth)))
	s.WriteString("\n")

	return s.String()
}

func displayWidth(s string) int {
	return lipgloss.Width(s)
}

func truncate(s string, max int) string {
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

func pad(s string, width int) string {
	w := displayWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeft(s string, width int) string {
	w := displayWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
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
	result := ""
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
		result += string(r)
	}
	return result
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

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	finalModel, err := p.Run()
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

		// Write path for shell wrapper to cd into
		os.WriteFile(selectionFile, []byte(repoPath), 0644)
	} else {
		// No selection - clean up any stale selection file
		os.Remove(selectionFile)
	}
}
