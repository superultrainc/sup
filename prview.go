package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// glamourStyle is resolved before the TUI starts; querying the terminal mid-program garbles input.
var glamourStyle = "dark"

type ghActor struct {
	Login string `json:"login"`
}

// BodyHTML fields carry GitHub's signed image URLs, which private-repo
// attachments need; see images.go.
type prComment struct {
	Author    ghActor   `json:"author"`
	Body      string    `json:"body"`
	BodyHTML  string    `json:"bodyHTML"`
	CreatedAt time.Time `json:"createdAt"`
	DiffHunk  string    `json:"diffHunk"`
}

type prReview struct {
	Author      ghActor   `json:"author"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	BodyHTML    string    `json:"bodyHTML"`
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
	CheckSuite *struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"` // CheckRun
}

// displayName is "Workflow / job" for Actions runs, like GitHub's checks list.
func (c checkContext) displayName() string {
	if c.Typename == "StatusContext" {
		return c.Context
	}
	if c.CheckSuite != nil && c.CheckSuite.WorkflowRun != nil && c.CheckSuite.WorkflowRun.Workflow.Name != "" {
		return c.CheckSuite.WorkflowRun.Workflow.Name + " / " + c.Name
	}
	return c.Name
}

func (c checkContext) result() string {
	switch {
	case c.Typename == "StatusContext":
		return c.State
	case c.Conclusion != "":
		return c.Conclusion
	}
	return c.Status
}

type prCommit struct {
	Commit struct {
		AbbreviatedOid  string    `json:"abbreviatedOid"`
		MessageHeadline string    `json:"messageHeadline"`
		CommittedDate   time.Time `json:"committedDate"`
		Author          struct {
			Name string   `json:"name"`
			User *ghActor `json:"user"`
		} `json:"author"`
		StatusCheckRollup *struct {
			State string `json:"state"`
		} `json:"statusCheckRollup"`
	} `json:"commit"`
}

func (c prCommit) authorName() string {
	if u := c.Commit.Author.User; u != nil && u.Login != "" {
		return u.Login
	}
	return c.Commit.Author.Name
}

type prLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"` // hex without #
}

type prDetail struct {
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	BodyHTML       string    `json:"bodyHTML"`
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
	HeadRefOid     string    `json:"headRefOid"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// mergeStateStatus is GitHub's merge-button state: CLEAN, BLOCKED, BEHIND, DIRTY, UNSTABLE, HAS_HOOKS, DRAFT, UNKNOWN.
	MergeStateStatus      string `json:"mergeStateStatus"`
	ViewerCanMergeAsAdmin bool   `json:"viewerCanMergeAsAdmin"`
	AutoMergeRequest      *struct {
		MergeMethod string `json:"mergeMethod"`
	} `json:"autoMergeRequest"`
	Repository struct {
		MergeCommitAllowed       bool   `json:"mergeCommitAllowed"`
		SquashMergeAllowed       bool   `json:"squashMergeAllowed"`
		RebaseMergeAllowed       bool   `json:"rebaseMergeAllowed"`
		ViewerDefaultMergeMethod string `json:"viewerDefaultMergeMethod"`
	} `json:"repository"`
	Labels struct {
		Nodes []prLabel `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []ghActor `json:"nodes"`
	} `json:"assignees"`
	AllCommits struct {
		Nodes []prCommit `json:"nodes"`
	} `json:"allCommits"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
				Name  string `json:"name"`
				Slug  string `json:"slug"` // teams only
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
			title body bodyHTML url state isDraft createdAt
			author { login }
			baseRefName headRefName additions deletions changedFiles reviewDecision mergeable
			headRefOid updatedAt mergeStateStatus viewerCanMergeAsAdmin
			autoMergeRequest { mergeMethod }
			repository { mergeCommitAllowed squashMergeAllowed rebaseMergeAllowed viewerDefaultMergeMethod }
			labels(first: 20) { nodes { name color } }
			assignees(first: 10) { nodes { login } }
			allCommits: commits(first: 100) { nodes { commit {
				abbreviatedOid messageHeadline committedDate
				author { name user { login } }
				statusCheckRollup { state }
			} } }
			reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } ... on Team { name slug } } } }
			files(first: 100) { nodes { path additions deletions } }
			commits(last: 1) { totalCount nodes { commit { statusCheckRollup { state contexts(first: 100) { nodes {
				__typename
				... on CheckRun { name conclusion status checkSuite { workflowRun { workflow { name } } } }
				... on StatusContext { context state }
			} } } } } }
			comments(first: 100) { nodes { author { login } body bodyHTML createdAt } }
			reviews(first: 100) { nodes { author { login } state body bodyHTML submittedAt } }
			reviewThreads(first: 100) { nodes {
				path line startLine originalLine diffSide isResolved isOutdated
				comments(first: 50) { nodes { author { login } body bodyHTML createdAt diffHunk } }
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

// Tab order; the diff comes right after the conversation.
const (
	tabConversation = iota
	tabFiles
	tabCommits
	tabChecks
	tabCount
)

type prDetailView struct {
	pr          PR
	detail      *prDetail
	loading     bool
	err         error
	tab         int
	panes       [tabCount]detailPane
	sidebar     []string // conversation's right column; doesn't scroll
	threadMarks []int    // n / N jump targets in the files tab
	diff        []*diffFile
	diffLoading bool
	diffErr     error
	layout      string // auto, split, unified
	wrap        bool
	collapsed   map[string]bool // per-path override of the default collapse rule
	width       int
	picker      *reviewerPicker // non-nil while the reviewer picker is open
	merge       *mergeDialog    // non-nil while the merge dialog is open
	search      *detailSearch   // non-nil while a `/` search is being typed or is active
	filesDirty  bool            // diff pane needs re-rendering before it's shown
}

func newDetailView(pr PR) *prDetailView {
	return &prDetailView{pr: pr, loading: true, diffLoading: true, layout: "auto", wrap: true, collapsed: map[string]bool{}}
}

const (
	detailHeaderHeight = 4 // title, state line, tabs, rule
	sidebarWidth       = 34
	treeWidth          = 40
)

func (v *prDetailView) sidebarWidth() int {
	if v.width >= 120 {
		return sidebarWidth
	}
	return 0
}

func (v *prDetailView) treeWidth() int {
	if v.width >= 150 && len(v.diff) > 1 {
		return treeWidth
	}
	return 0
}

// mainWidth is the scrolling column's width for a tab, after any side column.
func (v *prDetailView) mainWidth(tab int) int {
	switch {
	case tab == tabConversation && v.sidebarWidth() > 0:
		return v.width - v.sidebarWidth() - 1
	case tab == tabFiles && v.treeWidth() > 0:
		return v.width - v.treeWidth() - 1
	}
	return v.width
}

func (v *prDetailView) pane() *detailPane { return &v.panes[v.tab] }

func (v *prDetailView) bodyHeight(termHeight int) int {
	return max(termHeight-detailHeaderHeight-2, 3) // 2 footer rows
}

func (v *prDetailView) maxScroll(termHeight int) int {
	return max(len(v.pane().lines)-v.bodyHeight(termHeight), 0)
}

func (v *prDetailView) clampScroll(termHeight int) {
	for i := range v.panes {
		p := &v.panes[i]
		limit := len(p.lines) - v.bodyHeight(termHeight)
		// Let the last files reach the top so jumping to them highlights them in the tree.
		if i == tabFiles && len(p.marks) > 0 {
			limit = max(limit, p.marks[len(p.marks)-1])
		}
		p.scroll = max(min(p.scroll, limit), 0)
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
	return v.mainWidth(tabFiles) >= 150
}

// applyDetail swaps in new PR data, re-rendering the diff only when review
// threads (the one thing it shows from the detail) changed; that pane is the expensive one.
func (v *prDetailView) applyDetail(d *prDetail) {
	threadsChanged := v.detail == nil || threadsKey(v.detail) != threadsKey(d)
	v.detail = d
	v.width = max(v.width, 40)
	v.rebuildConv()
	v.rebuildCommits()
	v.rebuildChecks()
	if threadsChanged {
		v.invalidateFiles()
	}
	v.refreshSearch()
}

// rebuildForImages re-renders the panes that can show the newly loaded images.
func (v *prDetailView) rebuildForImages() {
	v.width = max(v.width, 40)
	v.rebuildConv()
	if v.detail != nil {
		for _, t := range v.detail.ReviewThreads.Nodes {
			for _, c := range t.Comments.Nodes {
				if len(findImages(c.Body)) > 0 {
					v.invalidateFiles()
					v.refreshSearch()
					return
				}
			}
		}
	}
	v.refreshSearch()
}

// threadsKey summarizes what the diff pane renders from review threads.
func threadsKey(d *prDetail) string {
	var sb strings.Builder
	for _, t := range d.ReviewThreads.Nodes {
		line := 0
		if t.Line != nil {
			line = *t.Line
		}
		fmt.Fprintf(&sb, "%s:%d:%v:%v|", t.Path, line, t.IsResolved, t.IsOutdated)
		for _, c := range t.Comments.Nodes {
			sb.WriteString(c.Author.Login + "\x00" + c.Body + "\x00")
		}
	}
	return sb.String()
}

func (v *prDetailView) rebuild(width int) {
	// Width is 0 until the first WindowSizeMsg; cards need room for their borders.
	v.width = max(width, 40)
	v.rebuildConv()
	v.rebuildCommits()
	v.rebuildChecks()
	v.invalidateFiles()
	v.refreshSearch()
}

// invalidateFiles marks the diff pane stale; it's the costly one, so it's only
// rendered when the Files tab is on screen (see ensureFiles).
func (v *prDetailView) invalidateFiles() {
	v.filesDirty = true
}

// ensureFiles renders a stale diff pane if the Files tab is showing; Update
// calls it after every message.
func (v *prDetailView) ensureFiles() {
	if v.filesDirty && v.tab == tabFiles {
		v.rebuildFilesKeepingPlace()
	}
}

// currentFile is the index of the file at the top of the files pane, or -1.
func (v *prDetailView) currentFile() int {
	cur := -1
	for i, mk := range v.panes[tabFiles].marks {
		if mk <= v.panes[tabFiles].scroll+1 {
			cur = i
		}
	}
	return cur
}

// rebuildFilesKeepingPlace re-renders after a layout toggle and returns to the same file.
func (v *prDetailView) rebuildFilesKeepingPlace() {
	cur := v.currentFile()
	v.rebuildFiles()
	if p := &v.panes[tabFiles]; cur >= 0 && cur < len(p.marks) {
		p.scroll = p.marks[cur]
	}
	v.refreshSearch()
}

func (v *prDetailView) rebuildFiles() {
	v.filesDirty = false
	p := &v.panes[tabFiles]
	if v.diff == nil {
		p.lines, p.marks, v.threadMarks = nil, nil, nil
		return
	}
	r := &diffRenderer{th: currentDiffTheme(), width: v.mainWidth(tabFiles) - 2, split: v.isSplit(), wrap: v.wrap,
		collapsed: v.collapsed, noSummary: v.treeWidth() > 0}
	if v.detail != nil {
		r.threads = v.detail.ReviewThreads.Nodes
	}
	r.render(v.diff)
	p.lines, p.marks, v.threadMarks = r.lines, r.fileMarks, r.threadMarks
}

func (m model) handleDetailInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.detail.picker != nil {
		return m.handlePickerInput(msg)
	}
	if m.detail.merge != nil {
		return m.handleMergeInput(msg)
	}
	if s := m.detail.search; s != nil && s.typing {
		return m.handleSearchInput(msg)
	}
	if m.confirmAction != "" {
		return m.handleNormalInput(msg)
	}
	v := m.detail
	p := v.pane()
	page := v.bodyHeight(m.height)
	switch key := msg.String(); key {
	case "esc", "q", "backspace", "left":
		// An active search clears first; only then does esc leave the view.
		if v.search != nil && key == "esc" {
			v.search = nil
			return m, nil
		}
		m.detail = nil
		m.actionStatus = ""
		return m, nil
	case "/":
		v.search = &detailSearch{typing: true, origin: p.scroll, current: -1}
		return m, nil
	case "tab":
		v.tab = (v.tab + 1) % tabCount
		v.refreshSearch()
	case "shift+tab":
		v.tab = (v.tab + tabCount - 1) % tabCount
		v.refreshSearch()
	case "1", "2", "3", "4":
		v.tab = int(key[0] - '1')
		v.refreshSearch()
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
	case "n", "N":
		dir := 1
		if key == "N" {
			dir = -1
		}
		// n/N follow search matches when a search is active, else review threads.
		switch {
		case v.search != nil:
			v.stepMatch(dir, m.height)
			return m, nil
		case v.tab == tabFiles:
			jumpMark(p, v.threadMarks, dir)
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
	case "m":
		v.merge = newMergeDialog(v.detail)
		m.actionStatus = ""
		return m, nil
	case "r":
		v.picker = newReviewerPicker(v.pr, v.detail)
		m.actionStatus = ""
		return m, tea.Batch(fetchReviewerCandidatesCmd(v.pr), spinnerTick())
	case "R":
		e := m.cacheEntry(prKey(v.pr))
		e.detailAt, e.head = time.Time{}, "" // force both to refetch
		v.loading, v.diffLoading = true, true
		return m, tea.Batch(append(m.fetchMissing(v.pr, e), spinnerTick())...)
	case "d":
		v.tab = tabFiles
	case "A", "D", "M", "o", "c":
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
	d.Commits.TotalCount = 3
	d.HeadRefOid, d.MergeStateStatus = "c7d8e9f", "CLEAN"
	d.Repository.SquashMergeAllowed, d.Repository.MergeCommitAllowed = true, true
	d.Repository.ViewerDefaultMergeMethod = "SQUASH"
	d.Labels.Nodes = []prLabel{{"enhancement", "a2eeef"}, {"backend", "5319e7"}}
	d.Assignees.Nodes = []ghActor{{pr.Author.Login}}
	for i, h := range []string{"Validate Config in NewThing", "Add NewThing tests", "Wrap /things in a 5s timeout"} {
		var c prCommit
		c.Commit.AbbreviatedOid = []string{"a1b2c3d", "e4f5a6b", "c7d8e9f"}[i]
		c.Commit.MessageHeadline = h
		c.Commit.CommittedDate = now.Add(time.Duration(-70+i*12) * time.Hour)
		c.Commit.Author.User = &ghActor{pr.Author.Login}
		d.AllCommits.Nodes = append(d.AllCommits.Nodes, c)
	}
	rollup := &struct {
		State    string `json:"state"`
		Contexts struct {
			Nodes []checkContext `json:"nodes"`
		} `json:"contexts"`
	}{State: "SUCCESS"}
	for _, n := range []string{"build", "test", "lint"} {
		rollup.Contexts.Nodes = append(rollup.Contexts.Nodes, checkContext{Typename: "CheckRun", Name: n, Conclusion: "SUCCESS"})
	}
	d.Commits.Nodes = append(d.Commits.Nodes, struct {
		Commit struct {
			StatusCheckRollup *struct {
				State    string `json:"state"`
				Contexts struct {
					Nodes []checkContext `json:"nodes"`
				} `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	}{})
	d.Commits.Nodes[0].Commit.StatusCheckRollup = rollup
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
