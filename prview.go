package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
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

// detailPane is one scrollable tab. lines are rebuilt in Update (on load,
// resize, or layout toggles) so View stays a pure function.
type detailPane struct {
	lines  []string
	marks  []int // [ / ] jump targets
	scroll int
}

const (
	tabConversation = iota
	tabFiles
)

type prDetailView struct {
	pr          PR
	detail      *prDetail
	loading     bool
	err         error
	tab         int
	conv, files detailPane
	activity    int   // timeline item count, shown on the tab
	threadMarks []int // n / N jump targets in the files tab
	diff        []*diffFile
	diffLoading bool
	diffErr     error
	layout      string // auto, split, unified
	wrap        bool
	collapsed   map[string]bool // per-path override of the default collapse rule
	width       int
}

func newDetailView(pr PR) *prDetailView {
	return &prDetailView{pr: pr, loading: true, diffLoading: true, layout: "auto", wrap: true, collapsed: map[string]bool{}}
}

var (
	sectionHeadStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	authorStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	metaStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pathStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	activeTabStyle   = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("205"))
)

// detailHeaderHeight is the fixed rows above the body: header card (6) + tabs (1).
const detailHeaderHeight = 7

func (v *prDetailView) pane() *detailPane {
	if v.tab == tabFiles {
		return &v.files
	}
	return &v.conv
}

func (v *prDetailView) bodyHeight(termHeight int) int {
	return max(termHeight-detailHeaderHeight-2, 3) // 2 footer rows
}

func (v *prDetailView) maxScroll(termHeight int) int {
	return max(len(v.pane().lines)-v.bodyHeight(termHeight), 0)
}

func (v *prDetailView) clampScroll(termHeight int) {
	for _, p := range []*detailPane{&v.conv, &v.files} {
		p.scroll = min(p.scroll, max(len(p.lines)-v.bodyHeight(termHeight), 0))
		p.scroll = max(p.scroll, 0)
	}
}

func jumpMark(p *detailPane, marks []int, dir int) {
	if dir > 0 {
		for _, s := range marks {
			if s > p.scroll {
				p.scroll = s
				return
			}
		}
		return
	}
	for i := len(marks) - 1; i >= 0; i-- {
		if marks[i] < p.scroll {
			p.scroll = marks[i]
			return
		}
	}
	p.scroll = 0
}

func (v *prDetailView) isSplit() bool {
	switch v.layout {
	case "split":
		return true
	case "unified":
		return false
	}
	return v.width >= 150
}

func (v *prDetailView) rebuild(width int) {
	v.width = width
	v.rebuildConv()
	v.rebuildFiles()
}

// currentFile is the index of the file card at the top of the files pane, or -1.
func (v *prDetailView) currentFile() int {
	cur := -1
	for i, mk := range v.files.marks {
		if mk <= v.files.scroll+1 {
			cur = i
		}
	}
	return cur
}

// rebuildFilesKeepingPlace re-renders after a layout toggle and returns to the same file.
func (v *prDetailView) rebuildFilesKeepingPlace() {
	cur := v.currentFile()
	v.rebuildFiles()
	if cur >= 0 && cur < len(v.files.marks) {
		v.files.scroll = v.files.marks[cur]
	}
}

func (v *prDetailView) rebuildFiles() {
	if v.diff == nil {
		v.files.lines, v.files.marks, v.threadMarks = nil, nil, nil
		return
	}
	r := &diffRenderer{th: currentDiffTheme(), width: v.width - 2, split: v.isSplit(), wrap: v.wrap, collapsed: v.collapsed}
	if v.detail != nil {
		r.threads = v.detail.ReviewThreads.Nodes
	}
	r.render(v.diff)
	v.files.lines, v.files.marks, v.threadMarks = r.lines, r.fileMarks, r.threadMarks
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

func reviewBorder(state string, th diffTheme) string {
	switch state {
	case "APPROVED":
		return th.addBar
	case "CHANGES_REQUESTED":
		return th.delBar
	}
	return th.border
}

// timelineItem is one entry in the activity feed, sorted by time.
type timelineItem struct {
	at     time.Time
	render func(width int) []string
}

func (v *prDetailView) rebuildConv() {
	d := v.detail
	if d == nil {
		v.conv.lines, v.conv.marks = nil, nil
		return
	}
	th := currentDiffTheme()
	cw := v.width - 2 // card width; lines get a 1-col left margin
	var lines []string
	var marks []int
	add := func(ls ...string) {
		for _, l := range ls {
			lines = append(lines, " "+l)
		}
	}

	marks = append(marks, len(lines))
	desc := markdownLines(d.Body, cw-4)
	if len(desc) == 0 {
		desc = []string{metaStyle.Render("No description provided.")}
	}
	add(textCard(sectionHeadStyle.Render("Description"), th.border, desc, cw)...)

	if checks, summary := checkLines(d); len(checks) > 0 {
		add("")
		marks = append(marks, len(lines))
		add(textCard(sectionHeadStyle.Render("Checks")+metaStyle.Render(" · ")+summary, th.border, checks, cw)...)
	}

	var items []timelineItem
	for _, c := range d.Comments.Nodes {
		c := c
		items = append(items, timelineItem{c.CreatedAt, func(w int) []string {
			title := commentedStyle.Render("●") + " " + authorStyle.Render(c.Author.Login) + metaStyle.Render(" commented · "+relTime(c.CreatedAt))
			return textCard(title, th.border, markdownLines(c.Body, w-4), w)
		}})
	}
	for _, r := range d.Reviews.Nodes {
		r := r
		// Empty COMMENTED reviews only wrap inline threads, which appear on their own.
		if r.State == "PENDING" || (r.State == "COMMENTED" && cleanBody(r.Body) == "") {
			continue
		}
		items = append(items, timelineItem{r.SubmittedAt, func(w int) []string {
			label, style := reviewStateLabel(r.State)
			title := authorStyle.Render(r.Author.Login) + " " + style.Render(label) + metaStyle.Render(" · "+relTime(r.SubmittedAt))
			body := markdownLines(r.Body, w-4)
			if len(body) == 0 {
				return []string{"  " + title}
			}
			return textCard(title, reviewBorder(r.State, th), body, w)
		}})
	}
	style := styles.Get(th.chroma)
	for _, t := range d.ReviewThreads.Nodes {
		t := t
		if len(t.Comments.Nodes) == 0 {
			continue
		}
		items = append(items, timelineItem{t.Comments.Nodes[0].CreatedAt, func(w int) []string {
			return threadCard(t, threadLocation(t), threadSnippet(t, style, w-4, th), w, th)
		}})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	v.activity = len(items)

	add("")
	marks = append(marks, len(lines))
	add(sectionHeadStyle.Render("Activity") + metaStyle.Render(fmt.Sprintf(" · %d", len(items))))
	if len(items) == 0 {
		add(metaStyle.Render("  No comments or reviews yet."))
	}
	connector := sty(th.divider, "", false, false).Render("  │")
	for _, it := range items {
		add(connector)
		marks = append(marks, len(lines))
		add(it.render(cw)...)
	}
	v.conv.lines, v.conv.marks = lines, marks
}

func threadLocation(t reviewThread) string {
	loc := t.Path
	switch {
	case t.Line != nil:
		loc = fmt.Sprintf("%s:%d", t.Path, *t.Line)
	case t.OriginalLine != nil:
		loc = fmt.Sprintf("%s:%d", t.Path, *t.OriginalLine)
	}
	if t.IsOutdated {
		loc += " · outdated"
	}
	return pathStyle.Render(loc)
}

// threadSnippet renders the last few diff lines a thread is attached to.
func threadSnippet(t reviewThread, style *chroma.Style, width int, th diffTheme) []string {
	hunk := t.Comments.Nodes[0].DiffHunk
	if hunk == "" {
		return nil
	}
	files := parseUnifiedDiff(fmt.Sprintf("diff --git a/%[1]s b/%[1]s\n--- a/%[1]s\n+++ b/%[1]s\n%[2]s", t.Path, hunk))
	if len(files) == 0 || len(files[0].hunks) == 0 {
		return nil
	}
	f := files[0]
	highlightFile(f, style)
	lines := f.hunks[0].lines
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	r := &diffRenderer{th: th, width: width + 2}
	nw := numWidth(f)
	var out []string
	for i := range lines {
		out = append(out, r.unifiedRows(&lines[i], nw)...)
	}
	return out
}

func checkLines(d *prDetail) ([]string, string) {
	if len(d.Commits.Nodes) == 0 || d.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return nil, ""
	}
	var out []string
	passed, failed, pending := 0, 0, 0
	for _, c := range d.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
		name, result := c.Name, c.Conclusion
		if c.Typename == "StatusContext" {
			name, result = c.Context, c.State
		} else if result == "" {
			result = c.Status
		}
		switch checkState(result) {
		case 1:
			passed++
		case -1:
			failed++
		default:
			pending++
		}
		out = append(out, checkIcon(result)+" "+name+metaStyle.Render(" "+strings.ToLower(result)))
	}
	var parts []string
	if passed > 0 {
		parts = append(parts, approvedStyle.Render(fmt.Sprintf("✓ %d passed", passed)))
	}
	if failed > 0 {
		parts = append(parts, changesRequestedStyle.Render(fmt.Sprintf("✗ %d failed", failed)))
	}
	if pending > 0 {
		parts = append(parts, reviewRequestedStyle.Render(fmt.Sprintf("● %d pending", pending)))
	}
	return out, strings.Join(parts, "  ")
}

// checkState is 1 for passed, -1 for failed, 0 for pending.
func checkState(result string) int {
	switch result {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return 1
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return -1
	}
	return 0
}

func checkIcon(result string) string {
	switch checkState(result) {
	case 1:
		return approvedStyle.Render("✓")
	case -1:
		return changesRequestedStyle.Render("✗")
	}
	return reviewRequestedStyle.Render("●")
}

func (m model) detailView() string {
	v := m.detail
	d := v.detail
	th := currentDiffTheme()
	var s strings.Builder
	cw := max(m.width-2, 40)

	slug := fmt.Sprintf("%s/%s #%d", v.pr.Repository.Owner.Login, v.pr.Repository.Name, v.pr.Number)
	cardTitle := titleStyle.Render("sup") + metaStyle.Render(" › ") + normalStyle.Render(slug) + "  " + getStatusBadge(v.pr)

	title, author, head, base := v.pr.Title, v.pr.Author.Login, v.pr.HeadRefName, ""
	adds, dels := v.pr.Additions, v.pr.Deletions
	if d != nil {
		title, author, head, base = d.Title, d.Author.Login, d.HeadRefName, d.BaseRefName
		adds, dels = d.Additions, d.Deletions
	}
	branches := branchStyle.Render(head)
	if base != "" {
		branches = branchStyle.Render(base) + metaStyle.Render(" ← ") + branches
	}
	meta := authorStyle.Render(author) + metaStyle.Render(" wants to merge ") + branches + "  " +
		additionsStyle.Render(fmt.Sprintf("+%d", adds)) + " " + deletionsStyle.Render(fmt.Sprintf("-%d", dels))
	if d != nil {
		meta += metaStyle.Render(fmt.Sprintf(" · %d commits · opened %s", d.Commits.TotalCount, relTime(d.CreatedAt)))
	}
	header := []string{
		selectedNormalStyle.Render(title),
		meta,
		m.detailStatusLine(d),
		detailReviewersLine(v.pr, d),
	}
	for _, l := range textCard(cardTitle, th.border, header, cw) {
		s.WriteString(" " + l + "\n")
	}
	s.WriteString(m.detailTabs() + "\n")

	p := v.pane()
	bodyH := v.bodyHeight(m.height)
	var placeholder string
	switch {
	case v.tab == tabConversation && v.err != nil && d == nil:
		placeholder = changesRequestedStyle.Render("  Error: " + v.err.Error())
	case v.tab == tabConversation && d == nil:
		placeholder = reviewRequestedStyle.Render("  " + spinnerFrames[m.spinnerFrame] + " Loading PR...")
	case v.tab == tabFiles && v.diffErr != nil && v.diff == nil:
		placeholder = changesRequestedStyle.Render("  Error: " + v.diffErr.Error())
	case v.tab == tabFiles && v.diff == nil:
		placeholder = reviewRequestedStyle.Render("  " + spinnerFrames[m.spinnerFrame] + " Loading diff...")
	}
	if placeholder != "" {
		s.WriteString("\n" + placeholder + "\n")
		bodyH -= 2
	} else {
		end := min(p.scroll+bodyH, len(p.lines))
		for _, l := range p.lines[p.scroll:end] {
			s.WriteString(l + "\n")
		}
		bodyH -= end - p.scroll
	}
	if bodyH > 0 {
		s.WriteString(strings.Repeat("\n", bodyH))
	}

	s.WriteString(m.detailFooter())
	return s.String()
}

func (m model) detailTabs() string {
	v := m.detail
	tab := func(label string, count, idx int) string {
		text := fmt.Sprintf("%s %d", label, count)
		if v.tab == idx {
			return activeTabStyle.Render(text)
		}
		return metaStyle.Render(text)
	}
	files := len(v.diff)
	if v.diff == nil && v.detail != nil {
		files = v.detail.ChangedFiles
	}
	line := "  " + tab("Conversation", v.activity, tabConversation) + "    " + tab("Files changed", files, tabFiles)
	if v.tab == tabFiles {
		layout := "unified"
		if v.isSplit() {
			layout = "split"
		}
		if v.layout == "auto" {
			layout += " (auto)"
		}
		wrap := "wrap"
		if !v.wrap {
			wrap = "no wrap"
		}
		right := metaStyle.Render(layout + " · " + wrap)
		gap := m.width - 1 - lipgloss.Width(line) - lipgloss.Width(right)
		if gap > 1 {
			line += strings.Repeat(" ", gap) + right
		}
	}
	return line
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
		pct = fmt.Sprintf("%d%%", v.pane().scroll*100/ms)
	}
	help := " j/k scroll · [/] next item · tab files · d hunk · A approve · D changes · M comment · o open · esc back"
	if v.tab == tabFiles {
		help = " j/k scroll · [/] file · n/N comment · s layout · w wrap · x collapse · tab conversation · esc back"
	}
	return status + "\n" + helpStyle.Render(truncateToWidth(help+"  "+pct, max(m.width-1, 20)))
}

func (m model) handleDetailInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmAction != "" {
		return m.handleNormalInput(msg)
	}
	v := m.detail
	p := v.pane()
	page := v.bodyHeight(m.height)
	switch msg.String() {
	case "esc", "q", "backspace", "left":
		m.detail = nil
		m.diffError = ""
		m.actionStatus = ""
		return m, nil
	case "tab", "shift+tab":
		v.tab = 1 - v.tab
	case "1":
		v.tab = tabConversation
	case "2":
		v.tab = tabFiles
	case "j", "down":
		p.scroll++
	case "k", "up":
		p.scroll--
	case "ctrl+d":
		p.scroll += page / 2
	case "ctrl+u":
		p.scroll -= page / 2
	case "ctrl+f", " ", "pgdown":
		p.scroll += page
	case "ctrl+b", "pgup":
		p.scroll -= page
	case "g", "home":
		p.scroll = 0
	case "G", "end":
		p.scroll = v.maxScroll(m.height)
	case "]":
		jumpMark(p, p.marks, 1)
	case "[":
		jumpMark(p, p.marks, -1)
	case "n":
		if v.tab == tabFiles {
			jumpMark(p, v.threadMarks, 1)
		}
	case "N":
		if v.tab == tabFiles {
			jumpMark(p, v.threadMarks, -1)
		}
	case "s":
		if v.tab == tabFiles {
			next := map[string]string{"auto": "split", "split": "unified", "unified": "auto"}
			v.layout = next[v.layout]
			v.rebuildFilesKeepingPlace()
		}
	case "w":
		if v.tab == tabFiles {
			v.wrap = !v.wrap
			v.rebuildFilesKeepingPlace()
		}
	case "x":
		if i := v.currentFile(); v.tab == tabFiles && i >= 0 && i < len(v.diff) {
			f := v.diff[i]
			r := diffRenderer{collapsed: v.collapsed}
			v.collapsed[f.path()] = !r.isCollapsed(f)
			v.rebuildFilesKeepingPlace()
		}
	case "R":
		v.loading, v.diffLoading = true, true
		return m, tea.Batch(fetchPRDetailCmd(v.pr), fetchPatchCmd(v.pr), spinnerTick())
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
		Body:           "## Summary\n\nValidates `Config` up front so `NewThing` can't return a half-built `Thing`.\n\n- Rejects empty names and non-positive timeouts\n- Wraps `/things` in a 5s timeout\n\n## Test plan\n\n- [x] `make test`\n- [ ] Manual QA on staging",
		State:          "OPEN",
		IsDraft:        pr.IsDraft,
		CreatedAt:      now.Add(-72 * time.Hour),
		Author:         ghActor{pr.Author.Login},
		BaseRefName:    "main",
		HeadRefName:    pr.HeadRefName,
		Additions:      pr.Additions,
		Deletions:      pr.Deletions,
		ChangedFiles:   4,
		ReviewDecision: pr.ReviewDecision,
		Mergeable:      "MERGEABLE",
	}
	d.Commits.TotalCount = 2
	d.Comments.Nodes = []prComment{
		{Author: ghActor{"mike"}, Body: "Nice. Does this need a migration?", CreatedAt: now.Add(-48 * time.Hour)},
		{Author: ghActor{pr.Author.Login}, Body: "Nope, no schema changes.", CreatedAt: now.Add(-47 * time.Hour)},
	}
	d.Reviews.Nodes = []prReview{
		{Author: ghActor{"mike"}, State: "APPROVED", Body: "LGTM 🚀", SubmittedAt: now.Add(-20 * time.Hour)},
		{Author: ghActor{"alex"}, State: "APPROVED", SubmittedAt: now.Add(-2 * time.Hour)},
	}
	t1 := reviewThread{Path: "internal/thing/thing.go", Line: line(42), DiffSide: "RIGHT", IsResolved: true}
	t1.Comments.Nodes = []prComment{
		{Author: ghActor{"alex"}, Body: "Should we wrap `ErrInvalidConfig` here so callers can `errors.Is` it?", CreatedAt: now.Add(-30 * time.Hour),
			DiffHunk: "@@ -38,8 +39,10 @@ type Thing struct {\n \n func NewThing(cfg Config) (*Thing, error) {\n-\tif cfg.Name == \"\" {\n-\t\treturn nil, errors.New(\"missing name\")\n+\tif cfg.Name == \"\" || cfg.Timeout <= 0 {\n+\t\treturn nil, fmt.Errorf(\"invalid config: %+v\", cfg)"},
		{Author: ghActor{pr.Author.Login}, Body: "Good call, done in the next commit.", CreatedAt: now.Add(-29 * time.Hour)},
	}
	t2 := reviewThread{Path: "internal/thing/thing_test.go", Line: line(6), DiffSide: "RIGHT"}
	t2.Comments.Nodes = []prComment{
		{Author: ghActor{"mike"}, Body: "nit: a table-driven test would make adding cases easier.", CreatedAt: now.Add(-21 * time.Hour),
			DiffHunk: "@@ -0,0 +1,6 @@\n+package thing\n+\n+import \"testing\"\n+\n+func TestNewThing(t *testing.T) {\n+\tif _, err := NewThing(Config{}); err == nil {"},
	}
	d.ReviewThreads.Nodes = []reviewThread{t1, t2}
	return d
}

const mockPatch = `diff --git a/internal/thing/thing.go b/internal/thing/thing.go
index 1111111..2222222 100644
--- a/internal/thing/thing.go
+++ b/internal/thing/thing.go
@@ -1,8 +1,9 @@
 package thing

 import (
-	"errors"
+	"context"
+	"errors"
 	"fmt"
 )

 // Thing does the thing.
@@ -38,8 +39,10 @@ type Thing struct {

 func NewThing(cfg Config) (*Thing, error) {
-	if cfg.Name == "" {
-		return nil, errors.New("missing name")
+	if cfg.Name == "" || cfg.Timeout <= 0 {
+		return nil, fmt.Errorf("invalid config: %+v", cfg)
 	}
 	t := &Thing{}
+	t.cfg = cfg
+	t.started = time.Now()
 	return t, nil
 }
diff --git a/internal/thing/thing_test.go b/internal/thing/thing_test.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/internal/thing/thing_test.go
@@ -0,0 +1,12 @@
+package thing
+
+import "testing"
+
+func TestNewThing(t *testing.T) {
+	if _, err := NewThing(Config{}); err == nil {
+		t.Fatal("expected error for empty config")
+	}
+	if _, err := NewThing(Config{Name: "a", Timeout: 1}); err != nil {
+		t.Fatalf("unexpected error: %v", err)
+	}
+}
diff --git a/cmd/server/router.go b/cmd/server/router.go
index 4444444..5555555 100644
--- a/cmd/server/router.go
+++ b/cmd/server/router.go
@@ -12,5 +12,5 @@ func routes(mux *http.ServeMux) {
 	mux.HandleFunc("/health", health)
 	mux.HandleFunc("/users", users)
-	mux.HandleFunc("/things", legacyThings)
+	mux.HandleFunc("/things", withTimeout(things, 5*time.Second))
 	mux.HandleFunc("/admin", admin)
 }
diff --git a/go.sum b/go.sum
index 6666666..7777777 100644
--- a/go.sum
+++ b/go.sum
@@ -1,2 +1,3 @@
 github.com/a/b v1.0.0 h1:abc=
+github.com/c/d v1.2.0 h1:def=
 github.com/a/b v1.0.0/go.mod h1:ghi=
`
