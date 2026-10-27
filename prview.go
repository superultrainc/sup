package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// glamourStyle is resolved before the TUI starts; querying the terminal mid-program garbles input.
var glamourStyle = "dark"

type ghActor struct {
	Login string `json:"login"`
}

type prComment struct {
	Author    ghActor   `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	DiffHunk  string    `json:"diffHunk"`
}

type prReview struct {
	Author      ghActor   `json:"author"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type reviewThread struct {
	Path         string `json:"path"`
	Line         *int   `json:"line"`
	StartLine    *int   `json:"startLine"`
	OriginalLine *int   `json:"originalLine"`
	DiffSide     string `json:"diffSide"`
	IsResolved   bool   `json:"isResolved"`
	IsOutdated   bool   `json:"isOutdated"`
	Comments     struct {
		Nodes []prComment `json:"nodes"`
	} `json:"comments"`
}

type prFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type checkContext struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`       // CheckRun
	Conclusion string `json:"conclusion"` // CheckRun
	Status     string `json:"status"`     // CheckRun
	Context    string `json:"context"`    // StatusContext
	State      string `json:"state"`      // StatusContext
}

type prDetail struct {
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	URL            string    `json:"url"`
	State          string    `json:"state"`
	IsDraft        bool      `json:"isDraft"`
	CreatedAt      time.Time `json:"createdAt"`
	Author         ghActor   `json:"author"`
	BaseRefName    string    `json:"baseRefName"`
	HeadRefName    string    `json:"headRefName"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changedFiles"`
	ReviewDecision string    `json:"reviewDecision"`
	Mergeable      string    `json:"mergeable"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Files struct {
		Nodes []prFile `json:"nodes"`
	} `json:"files"`
	Commits struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Comments struct {
		Nodes []prComment `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []prReview `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []reviewThread `json:"nodes"`
	} `json:"reviewThreads"`
}

const prDetailQuery = `{
	repository(owner: "%s", name: "%s") {
		pullRequest(number: %d) {
			title body url state isDraft createdAt
			author { login }
			baseRefName headRefName additions deletions changedFiles reviewDecision mergeable
			reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } ... on Team { name } } } }
			files(first: 100) { nodes { path additions deletions } }
			commits(last: 1) { totalCount nodes { commit { statusCheckRollup { state contexts(first: 100) { nodes {
				__typename
				... on CheckRun { name conclusion status }
				... on StatusContext { context state }
			} } } } } }
			comments(first: 100) { nodes { author { login } body createdAt } }
			reviews(first: 100) { nodes { author { login } state body submittedAt } }
			reviewThreads(first: 100) { nodes {
				path line startLine originalLine diffSide isResolved isOutdated
				comments(first: 50) { nodes { author { login } body createdAt diffHunk } }
			} }
		}
	}
}`

func fetchPRDetail(pr PR) (*prDetail, error) {
	if demoMode {
		return mockPRDetail(pr), nil
	}
	out, err := graphqlPOST(fmt.Sprintf(prDetailQuery, pr.Repository.Owner.Login, pr.Repository.Name, pr.Number))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest *prDetail `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse PR: %w", err)
	}
	if resp.Data.Repository.PullRequest == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("%s", resp.Errors[0].Message)
		}
		return nil, fmt.Errorf("PR not found")
	}
	return resp.Data.Repository.PullRequest, nil
}

type prDetailFetchedMsg struct {
	key    string
	detail *prDetail
	err    error
}

func fetchPRDetailCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		d, err := fetchPRDetail(pr)
		return prDetailFetchedMsg{key: prKey(pr), detail: d, err: err}
	}
}

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

func cleanBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = htmlCommentRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2, 2006")
	}
}

// prDetailView is the full-screen PR view state. lines is rebuilt in Update
// (on load and resize) so View stays a pure function.
type prDetailView struct {
	pr       PR
	detail   *prDetail
	loading  bool
	err      error
	scroll   int
	lines    []string
	sections []int // line index of each section heading, for [ / ]
	width    int
}

var (
	sectionHeadStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	authorStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	metaStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pathStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	hunkCtxStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// detailHeaderHeight is the number of fixed rows above the scrolling body.
const detailHeaderHeight = 7

func (v *prDetailView) bodyHeight(termHeight int) int {
	h := termHeight - detailHeaderHeight - 2 // footer rows
	if h < 3 {
		h = 3
	}
	return h
}

func (v *prDetailView) maxScroll(termHeight int) int {
	m := len(v.lines) - v.bodyHeight(termHeight)
	if m < 0 {
		return 0
	}
	return m
}

func (v *prDetailView) clampScroll(termHeight int) {
	if v.scroll > v.maxScroll(termHeight) {
		v.scroll = v.maxScroll(termHeight)
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
}

func (v *prDetailView) jumpSection(dir int) {
	if dir > 0 {
		for _, s := range v.sections {
			if s > v.scroll {
				v.scroll = s
				return
			}
		}
		return
	}
	for i := len(v.sections) - 1; i >= 0; i-- {
		if v.sections[i] < v.scroll {
			v.scroll = v.sections[i]
			return
		}
	}
	v.scroll = 0
}

// timelineItem is one entry in the activity section, sorted by time.
type timelineItem struct {
	at     time.Time
	render func(b *detailBuilder)
}

type detailBuilder struct {
	lines    []string
	sections []int
	width    int
	md       *glamour.TermRenderer
}

func (b *detailBuilder) line(s string) { b.lines = append(b.lines, s) }

func (b *detailBuilder) section(title string) {
	if len(b.lines) > 0 {
		b.line("")
	}
	b.sections = append(b.sections, len(b.lines))
	b.line(" " + sectionHeadStyle.Render(title))
	b.line(" " + metaStyle.Render(strings.Repeat("─", max(b.width-2, 10))))
}

// markdown renders body indented by indent spaces.
func (b *detailBuilder) markdown(body string, indent int) {
	body = cleanBody(body)
	if body == "" {
		b.line(strings.Repeat(" ", indent) + metaStyle.Render("(no description)"))
		return
	}
	out := body
	if b.md != nil {
		if r, err := b.md.Render(body); err == nil {
			out = r
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n "), "\n")
	for len(lines) > 0 && strings.TrimSpace(stripAnsi(lines[0])) == "" {
		lines = lines[1:]
	}
	pad := strings.Repeat(" ", indent)
	for _, l := range lines {
		b.line(pad + strings.TrimRight(l, " "))
	}
}

func reviewStateLabel(state string) (string, lipgloss.Style) {
	switch state {
	case "APPROVED":
		return "✓ approved", approvedStyle
	case "CHANGES_REQUESTED":
		return "✗ requested changes", changesRequestedStyle
	case "DISMISSED":
		return "dismissed review", draftStyle
	default:
		return "reviewed", commentedStyle
	}
}

func (v *prDetailView) rebuild(width int) {
	v.width = width
	d := v.detail
	if d == nil {
		v.lines, v.sections = nil, nil
		return
	}
	// glamour's document style adds a 2-col margin on each side.
	md, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(max(width-8, 20)),
		glamour.WithEmoji(),
	)
	b := &detailBuilder{width: width, md: md}

	b.section("Description")
	b.markdown(d.Body, 1)

	b.section("Checks")
	b.checks(d)

	b.section(fmt.Sprintf("Files (%d)", d.ChangedFiles))
	addW, delW := 0, 0
	for _, f := range d.Files.Nodes {
		addW = max(addW, len(fmt.Sprintf("+%d", f.Additions)))
		delW = max(delW, len(fmt.Sprintf("-%d", f.Deletions)))
	}
	for _, f := range d.Files.Nodes {
		adds := additionsStyle.Render(padLeft(fmt.Sprintf("+%d", f.Additions), addW))
		dels := deletionsStyle.Render(padLeft(fmt.Sprintf("-%d", f.Deletions), delW))
		b.line("   " + adds + " " + dels + "  " + pathStyle.Render(f.Path))
	}
	if d.ChangedFiles > len(d.Files.Nodes) {
		b.line("   " + metaStyle.Render(fmt.Sprintf("… %d more", d.ChangedFiles-len(d.Files.Nodes))))
	}

	var items []timelineItem
	for _, c := range d.Comments.Nodes {
		c := c
		items = append(items, timelineItem{at: c.CreatedAt, render: func(b *detailBuilder) {
			b.line(" " + commentedStyle.Render("●") + " " + authorStyle.Render(c.Author.Login) + metaStyle.Render(" commented · "+relTime(c.CreatedAt)))
			b.markdown(c.Body, 2)
		}})
	}
	for _, r := range d.Reviews.Nodes {
		r := r
		// Empty COMMENTED reviews are just containers for inline threads, shown below.
		if r.State == "PENDING" || (r.State == "COMMENTED" && cleanBody(r.Body) == "") {
			continue
		}
		items = append(items, timelineItem{at: r.SubmittedAt, render: func(b *detailBuilder) {
			label, style := reviewStateLabel(r.State)
			b.line(" " + style.Render("◆") + " " + authorStyle.Render(r.Author.Login) + " " + style.Render(label) + metaStyle.Render(" · "+relTime(r.SubmittedAt)))
			if cleanBody(r.Body) != "" {
				b.markdown(r.Body, 2)
			}
		}})
	}
	for _, t := range d.ReviewThreads.Nodes {
		t := t
		if len(t.Comments.Nodes) == 0 {
			continue
		}
		items = append(items, timelineItem{at: t.Comments.Nodes[0].CreatedAt, render: func(b *detailBuilder) {
			b.thread(t)
		}})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })

	b.section(fmt.Sprintf("Activity (%d)", len(items)))
	if len(items) == 0 {
		b.line(" " + metaStyle.Render("No comments or reviews yet."))
	}
	for i, it := range items {
		if i > 0 {
			b.line("")
		}
		it.render(b)
	}

	v.lines, v.sections = b.lines, b.sections
}

func (b *detailBuilder) checks(d *prDetail) {
	if len(d.Commits.Nodes) == 0 || d.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		b.line(" " + metaStyle.Render("No checks."))
		return
	}
	for _, c := range d.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
		name, result := c.Name, c.Conclusion
		if c.Typename == "StatusContext" {
			name, result = c.Context, c.State
		} else if result == "" {
			result = c.Status
		}
		b.line("   " + checkIcon(result) + " " + name + metaStyle.Render(" "+strings.ToLower(result)))
	}
}

func checkIcon(result string) string {
	switch result {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return approvedStyle.Render("✓")
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return changesRequestedStyle.Render("✗")
	default:
		return reviewRequestedStyle.Render("●")
	}
}

func (b *detailBuilder) thread(t reviewThread) {
	loc := t.Path
	if t.Line != nil {
		loc = fmt.Sprintf("%s:%d", t.Path, *t.Line)
	} else if t.OriginalLine != nil {
		loc = fmt.Sprintf("%s:%d", t.Path, *t.OriginalLine)
	}
	var flags []string
	if t.IsResolved {
		flags = append(flags, approvedStyle.Render("resolved"))
	}
	if t.IsOutdated {
		flags = append(flags, draftStyle.Render("outdated"))
	}
	head := " " + reviewRequestedStyle.Render("▸") + " " + pathStyle.Render(loc)
	if len(flags) > 0 {
		head += " " + strings.Join(flags, " ")
	}
	b.line(head)

	// Last few lines of the diff hunk give the comment its code context.
	if hunkText := t.Comments.Nodes[0].DiffHunk; hunkText != "" {
		hl := strings.Split(strings.TrimRight(hunkText, "\n"), "\n")
		if len(hl) > 4 {
			hl = hl[len(hl)-4:]
		}
		for _, l := range hl {
			style := hunkCtxStyle
			switch {
			case strings.HasPrefix(l, "+"):
				style = additionsStyle
			case strings.HasPrefix(l, "-"):
				style = deletionsStyle
			}
			b.line("   " + metaStyle.Render("│ ") + style.Render(truncate(l, max(b.width-8, 20))))
		}
	}
	for _, c := range t.Comments.Nodes {
		b.line("   " + authorStyle.Render(c.Author.Login) + metaStyle.Render(" · "+relTime(c.CreatedAt)))
		b.markdown(c.Body, 3)
	}
}

func (m model) detailView() string {
	v := m.detail
	d := v.detail
	var s strings.Builder
	width := max(m.width-2, 40)

	slug := fmt.Sprintf("%s/%s #%d", v.pr.Repository.Owner.Login, v.pr.Repository.Name, v.pr.Number)
	s.WriteString("\n " + titleStyle.Render("sup") + metaStyle.Render(" › ") + normalStyle.Render(slug) + "  " + getStatusBadge(v.pr) + "\n")

	title, author, head, base := v.pr.Title, v.pr.Author.Login, v.pr.HeadRefName, ""
	adds, dels := v.pr.Additions, v.pr.Deletions
	if d != nil {
		title, author, head, base = d.Title, d.Author.Login, d.HeadRefName, d.BaseRefName
		adds, dels = d.Additions, d.Deletions
	}
	s.WriteString(" " + selectedNormalStyle.Render(truncate(title, width)) + "\n")

	branches := branchStyle.Render(head)
	if base != "" {
		branches = branchStyle.Render(base) + metaStyle.Render(" ← ") + branches
	}
	meta := authorStyle.Render(author) + metaStyle.Render(" wants to merge ") + branches + "  " +
		additionsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + deletionsStyle.Render(fmt.Sprintf("-%d", dels))
	if d != nil {
		meta += metaStyle.Render(fmt.Sprintf(" · %d files · %d commits · opened %s", d.ChangedFiles, d.Commits.TotalCount, relTime(d.CreatedAt)))
	}
	s.WriteString(" " + meta + "\n")

	s.WriteString(" " + m.detailStatusLine(d) + "\n")
	s.WriteString(" " + detailReviewersLine(v.pr, d) + "\n")
	s.WriteString(" " + metaStyle.Render(strings.Repeat("─", width)) + "\n")

	bodyH := v.bodyHeight(m.height)
	switch {
	case v.err != nil && d == nil:
		s.WriteString(changesRequestedStyle.Render(" Error: "+v.err.Error()) + "\n")
		bodyH--
	case v.loading && d == nil:
		s.WriteString(reviewRequestedStyle.Render(" "+spinnerFrames[m.spinnerFrame]+" Loading PR...") + "\n")
		bodyH--
	default:
		end := min(v.scroll+bodyH, len(v.lines))
		for _, l := range v.lines[v.scroll:end] {
			s.WriteString(l + "\n")
		}
		bodyH -= end - v.scroll
	}
	if bodyH > 0 {
		s.WriteString(strings.Repeat("\n", bodyH))
	}

	s.WriteString(m.detailFooter())
	return s.String()
}

func (m model) detailStatusLine(d *prDetail) string {
	if d == nil {
		return ""
	}
	var parts []string
	state := strings.ToLower(d.State)
	if d.IsDraft {
		state = "draft"
	}
	parts = append(parts, metaStyle.Render("state ")+normalStyle.Render(state))
	if len(d.Commits.Nodes) > 0 && d.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		r := d.Commits.Nodes[0].Commit.StatusCheckRollup.State
		parts = append(parts, metaStyle.Render("checks ")+checkIcon(r)+" "+normalStyle.Render(strings.ToLower(r)))
	}
	switch d.Mergeable {
	case "MERGEABLE":
		parts = append(parts, metaStyle.Render("merge ")+approvedStyle.Render("clean"))
	case "CONFLICTING":
		parts = append(parts, metaStyle.Render("merge ")+changesRequestedStyle.Render("conflicts"))
	}
	threads, unresolved := 0, 0
	for _, t := range d.ReviewThreads.Nodes {
		threads++
		if !t.IsResolved {
			unresolved++
		}
	}
	if threads > 0 {
		parts = append(parts, metaStyle.Render("threads ")+normalStyle.Render(fmt.Sprintf("%d/%d unresolved", unresolved, threads)))
	}
	return strings.Join(parts, metaStyle.Render("  ·  "))
}

// detailReviewersLine shows each reviewer's latest state, then pending requests.
func detailReviewersLine(pr PR, d *prDetail) string {
	latest := map[string]string{}
	var order []string
	if d != nil {
		for _, r := range d.Reviews.Nodes {
			login := r.Author.Login
			if login == "" || login == d.Author.Login || r.State == "PENDING" {
				continue
			}
			if _, ok := latest[login]; !ok {
				order = append(order, login)
			}
			// A later COMMENTED review doesn't override an approval or change request.
			if prev := latest[login]; r.State != "COMMENTED" || prev == "" || prev == "COMMENTED" {
				latest[login] = r.State
			}
		}
	}
	var parts []string
	for _, login := range order {
		switch latest[login] {
		case "APPROVED":
			parts = append(parts, approvedStyle.Render("✓ "+login))
		case "CHANGES_REQUESTED":
			parts = append(parts, changesRequestedStyle.Render("✗ "+login))
		default:
			parts = append(parts, commentedStyle.Render("💬 "+login))
		}
	}
	requested := strings.Fields(getRequestedReviewerNames(pr))
	if d != nil {
		requested = nil
		for _, rr := range d.ReviewRequests.Nodes {
			name := rr.RequestedReviewer.Login
			if name == "" {
				name = rr.RequestedReviewer.Name
			}
			if name != "" {
				requested = append(requested, name)
			}
		}
	}
	for _, name := range requested {
		parts = append(parts, reviewRequestedStyle.Render("● "+name))
	}
	if len(parts) == 0 {
		return metaStyle.Render("reviewers ") + metaStyle.Render("none")
	}
	return metaStyle.Render("reviewers ") + strings.Join(parts, "  ")
}

func (m model) detailFooter() string {
	v := m.detail
	var status string
	switch {
	case m.confirmAction == "approve" && m.confirmPR != nil:
		status = filterStyle.Render(fmt.Sprintf(" Approve PR #%d? (y/n)", m.confirmPR.Number))
	case m.actionPending:
		status = reviewRequestedStyle.Render(" " + spinnerFrames[m.spinnerFrame] + " Submitting review...")
	case m.loadingDiff:
		status = reviewRequestedStyle.Render(" " + spinnerFrames[m.spinnerFrame] + " Loading diff...")
	case m.actionStatus != "":
		style := approvedStyle
		if strings.HasPrefix(m.actionStatus, "Error") || strings.Contains(m.actionStatus, "cancelled") {
			style = changesRequestedStyle
		}
		status = style.Render(" " + m.actionStatus)
	case m.diffError != "":
		status = changesRequestedStyle.Render(" " + m.diffError)
	case v.err != nil && v.detail != nil:
		status = changesRequestedStyle.Render(" Reload failed: " + v.err.Error())
	case v.loading && v.detail != nil:
		status = reviewRequestedStyle.Render(" " + spinnerFrames[m.spinnerFrame] + " Refreshing...")
	}

	pct := "all"
	if ms := v.maxScroll(m.height); ms > 0 {
		pct = fmt.Sprintf("%d%%", v.scroll*100/ms)
	}
	help := fmt.Sprintf(" j/k scroll · [/] section · d diff · A approve · D changes · M comment · o open · esc back  %s", pct)
	return status + "\n" + helpStyle.Render(truncateToWidth(help, max(m.width-1, 20)))
}

func (m model) handleDetailInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmAction != "" {
		return m.handleNormalInput(msg)
	}
	v := m.detail
	page := v.bodyHeight(m.height)
	switch msg.String() {
	case "esc", "q", "backspace", "h", "left":
		m.detail = nil
		m.diffError = ""
		m.actionStatus = ""
		return m, nil
	case "j", "down":
		v.scroll++
	case "k", "up":
		v.scroll--
	case "ctrl+d":
		v.scroll += page / 2
	case "ctrl+u":
		v.scroll -= page / 2
	case "ctrl+f", " ", "pgdown":
		v.scroll += page
	case "ctrl+b", "pgup":
		v.scroll -= page
	case "g", "home":
		v.scroll = 0
	case "G", "end":
		v.scroll = v.maxScroll(m.height)
	case "]", "tab":
		v.jumpSection(1)
	case "[", "shift+tab":
		v.jumpSection(-1)
	case "R":
		v.loading = true
		return m, tea.Batch(fetchPRDetailCmd(v.pr), spinnerTick())
	case "d", "A", "D", "M", "o", "c":
		return m.handleNormalInput(msg)
	}
	v.clampScroll(m.height)
	return m, nil
}

func mockPRDetail(pr PR) *prDetail {
	line := func(n int) *int { return &n }
	now := time.Now()
	d := &prDetail{
		Title:          pr.Title,
		Body:           "## Summary\n\nThis PR adds the thing.\n\n- Adds `NewThing()` with tests\n- Wires it into the router\n\n## Test plan\n\n- [x] `make test`\n- [ ] Manual QA on staging",
		State:          "OPEN",
		IsDraft:        pr.IsDraft,
		CreatedAt:      now.Add(-72 * time.Hour),
		Author:         ghActor{pr.Author.Login},
		BaseRefName:    "main",
		HeadRefName:    pr.HeadRefName,
		Additions:      pr.Additions,
		Deletions:      pr.Deletions,
		ChangedFiles:   3,
		ReviewDecision: pr.ReviewDecision,
		Mergeable:      "MERGEABLE",
	}
	d.Commits.TotalCount = 2
	d.Files.Nodes = []prFile{
		{"internal/thing/thing.go", 120, 4},
		{"internal/thing/thing_test.go", 80, 0},
		{"cmd/server/router.go", 6, 2},
	}
	d.Comments.Nodes = []prComment{
		{Author: ghActor{"mike"}, Body: "Nice. Does this need a migration?", CreatedAt: now.Add(-48 * time.Hour)},
		{Author: ghActor{pr.Author.Login}, Body: "Nope, no schema changes.", CreatedAt: now.Add(-47 * time.Hour)},
	}
	d.Reviews.Nodes = []prReview{
		{Author: ghActor{"mike"}, State: "APPROVED", Body: "LGTM 🚀", SubmittedAt: now.Add(-20 * time.Hour)},
	}
	t := reviewThread{Path: "internal/thing/thing.go", Line: line(42), DiffSide: "RIGHT"}
	t.Comments.Nodes = []prComment{
		{Author: ghActor{"alex"}, Body: "Should this handle `nil`?", CreatedAt: now.Add(-30 * time.Hour),
			DiffHunk: "@@ -40,3 +40,5 @@ func NewThing() {\n \tt := &Thing{}\n+\tt.cfg = cfg\n+\treturn t"},
		{Author: ghActor{pr.Author.Login}, Body: "Good catch, fixed.", CreatedAt: now.Add(-29 * time.Hour)},
	}
	t.IsResolved = true
	d.ReviewThreads.Nodes = []reviewThread{t}
	return d
}
