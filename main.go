package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
		Name string `json:"name"`
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
	visibleCount int // for animation
}

type prsLoadedMsg struct {
	prs []PR
	err error
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(time.Millisecond*15, func(t time.Time) tea.Msg {
		return tickMsg{}
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

func initialModel() model {
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

func fetchPRs() tea.Msg {
	query := `{
		search(query: "org:superultrainc is:pr is:open", type: ISSUE, first: 100) {
			nodes {
				... on PullRequest {
					number
					title
					headRefName
					isDraft
					additions
					deletions
					author { login }
					repository { name }
					reviewDecision
					reviewRequests(first: 1) { totalCount nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
					reviews(last: 1) { nodes { author { login } state } }
				}
			}
		}
	}`

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
	return fetchPRs
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
			return m, nil
		}
		m.prs = msg.prs
		m.filtered = msg.prs
		m.loading = false
		m.visibleCount = 0
		return m, tick()

	case tickMsg:
		if m.visibleCount < len(m.filtered) {
			m.visibleCount += 2 // animate 2 at a time for speed
			if m.visibleCount > len(m.filtered) {
				m.visibleCount = len(m.filtered)
			}
			return m, tick()
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
			url := fmt.Sprintf("https://github.com/superultrainc/%s/pull/%d", pr.Repository.Name, pr.Number)
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
		s.WriteString(dimStyle.Render("  Loading..."))
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
	}

	s.WriteString("\n\n")
	s.WriteString(helpStyle.Render("  j/k ↑/↓: navigate • g/G: top/bottom • /: filter • o: open • enter: select • q/esc: quit"))
	s.WriteString("\n")

	return s.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
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
	// Output file for shell integration (just the directory path)
	outputFile := "/tmp/gpr-selection"
	devDir := os.Getenv("GPR_DEV_DIR")
	if devDir == "" {
		devDir = os.Getenv("HOME") + "/Development"
	}

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	m := finalModel.(model)
	if m.selected != nil {
		repoPath := devDir + "/" + m.selected.Repository.Name

		// Change to repo directory and run gh pr checkout
		if err := os.Chdir(repoPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not cd to %s: %v\n", repoPath, err)
			os.Exit(1)
		}

		cmd := exec.Command("gh", "pr", "checkout", fmt.Sprintf("%d", m.selected.Number), "--force")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: gh pr checkout failed: %v\n", err)
			os.Exit(1)
		}

		// Write just the path for shell to cd into
		os.WriteFile(outputFile, []byte(repoPath), 0644)
	} else {
		os.Remove(outputFile)
	}
}
