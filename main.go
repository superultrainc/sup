package main

import (
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
	orgs     []string // Auto-detected or from SUP_ORG
	mineMode bool     // Show PRs involving current user
	demoMode bool     // Show mock data for screenshots
)

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
	visibleCount int  // for animation
	spinnerFrame int  // for loading spinner
}

type prsLoadedMsg struct {
	prs []PR
	err error
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
			Bold(true).
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	draftStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	approvedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))

	changesRequestedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196"))

	reviewRequestedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214"))

	commentedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117"))

	openStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	additionsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))

	deletionsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	filterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	branchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("141"))
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
					reviewRequests(first: 1) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
					reviews(last: 1) { nodes { author { login } state } }
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

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchPRs, spinnerTick())
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
		if m.loading || m.refreshing {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerTick()
		}
		return m, nil

	case tea.KeyMsg:
		// Allow quitting even during animation
		if msg.String() == "q" || msg.String() == "ctrl+c" || msg.String() == "esc" {
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
		return m.handleNormalInput(msg)
	}

	return m, nil
}

func (m model) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filterMode = false
		m.filterText = ""
		m.filtered = m.prs
		m.cursor = 0
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
	m.filtered = []PR{}
	for _, pr := range m.prs {
		searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s",
			pr.Repository.Name, pr.Title, pr.Author.Login, pr.HeadRefName))
		if strings.Contains(searchText, filter) {
			m.filtered = append(m.filtered, pr)
		}
	}
	m.cursor = 0
}

func (m model) handleNormalInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		m.quitting = true
		return m, tea.Quit

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

	case "/":
		m.filterMode = true
		m.filterText = ""
		return m, nil

	case "enter":
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			m.selected = &m.filtered[m.cursor]
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case "o":
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			pr := m.filtered[m.cursor]
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
			exec.Command("open", url).Start()
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

func getDiffStats(pr PR) string {
	adds := additionsStyle.Render(fmt.Sprintf("+%d", pr.Additions))
	dels := deletionsStyle.Render(fmt.Sprintf("-%d", pr.Deletions))
	return fmt.Sprintf("%s %s", adds, dels)
}

func (m model) View() string {
	var s strings.Builder
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	// Column widths - defined early for header
	const (
		colStatus   = 12
		colRepo     = 28
		colNum      = 6
		colTitle    = 32
		colAuthor   = 14
		colReviewer = 14
		colBranch   = 20
	)

	s.WriteString("\n")

	if m.filterMode {
		s.WriteString(filterStyle.Render(fmt.Sprintf("  / %s█", m.filterText)))
		s.WriteString("\n\n")
	} else if m.filterText != "" {
		s.WriteString(filterStyle.Render(fmt.Sprintf("  Filter: %s", m.filterText)))
		s.WriteString("\n\n")
	}

	// Always show header
	s.WriteString(dimStyle.Render("  " + pad("STATUS", colStatus) + pad("REPO", colRepo) + pad("#", colNum) + pad("TITLE", colTitle) + pad("AUTHOR", colAuthor) + pad("REVIEWER", colReviewer) + pad("BRANCH", colBranch) + "+/-"))
	s.WriteString("\n")
	s.WriteString(dimStyle.Render("  " + strings.Repeat("─", colStatus+colRepo+colNum+colTitle+colAuthor+colReviewer+colBranch+10)))
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
				cursor = "> "
			}

			// Prepare padded values
			status := pad(stripAnsi(getStatusBadge(pr)), colStatus)
			repo := pad(truncate(pr.Repository.Name, colRepo-1), colRepo)
			num := pad(fmt.Sprintf("#%d", pr.Number), colNum)
			title := pad(truncate(pr.Title, colTitle-1), colTitle)
			author := pad(truncate(pr.Author.Login, colAuthor-1), colAuthor)
			reviewer := pad(truncate(getReviewer(pr), colReviewer-1), colReviewer)
			branch := pad(truncate(pr.HeadRefName, colBranch-1), colBranch)
			adds := fmt.Sprintf("+%d", pr.Additions)
			dels := fmt.Sprintf("-%d", pr.Deletions)

			if isSelected {
				line := cursor + status + repo + num + title + author + reviewer + branch + adds + " " + dels
				s.WriteString(selectedStyle.Render(line))
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
				s.WriteString(additionsStyle.Render(adds) + " ")
				s.WriteString(deletionsStyle.Render(dels))
			}
			s.WriteString("\n")
		}

		s.WriteString(fmt.Sprintf("\n  %d/%d PRs", len(m.filtered), len(m.prs)))
		if m.refreshing {
			spinner := spinnerFrames[m.spinnerFrame]
			s.WriteString(loadingStyle.Render("  " + spinner + " Refreshing"))
		}
	}

	s.WriteString("\n\n")
	s.WriteString(helpStyle.Render("  j/k ↑/↓: navigate • g/G: top/bottom • /: filter • o: open • enter: checkout • q/esc: quit"))
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
	} else if !mineMode {
		// Check for SUP_ORG override first
		if orgEnv := os.Getenv("SUP_ORG"); orgEnv != "" {
			orgs = strings.Split(orgEnv, ",")
		} else {
			// Auto-detect orgs from GitHub
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
		}
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
	}
}
