package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	sectionHeadStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	authorStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	metaStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	pathStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	prTitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
)

// bandRow fills a card's header row with the band color; left and right must
// already carry the band background or the fill breaks at their resets.
func bandRow(left, right string, width int, band string) string {
	fill := func(n int) string { return sty("", band, false, false).Render(strings.Repeat(" ", max(n, 0))) }
	content := fill(1) + left
	gap := width - lipgloss.Width(content) - lipgloss.Width(right) - 1
	if right == "" || gap < 2 {
		if lipgloss.Width(content) > width {
			content = ansi.Truncate(content, width, "")
		}
		return content + fill(width-lipgloss.Width(content))
	}
	return content + fill(gap) + right + fill(1)
}

// bandCard is GitHub's comment box: a shaded header row over a padded body.
func bandCard(left, right string, body []string, width int, border, band string) []string {
	bs := sty(border, "", false, false)
	inner := width - 2
	out := []string{
		bs.Render("╭" + strings.Repeat("─", inner) + "╮"),
		bs.Render("│") + bandRow(left, right, inner, band) + bs.Render("│"),
	}
	if len(body) > 0 {
		out = append(out, bs.Render("├"+strings.Repeat("─", inner)+"┤"))
		for _, l := range body {
			out = append(out, bs.Render("│")+" "+fitWidth(l, inner-2)+" "+bs.Render("│"))
		}
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

type timelineKind int

const (
	itemCard timelineKind = iota
	itemEvent
	itemCommit
)

type timelineItem struct {
	at     time.Time
	kind   timelineKind
	commit prCommit
	render func(width int) []string
}

func (v *prDetailView) rebuildConv() {
	p := &v.panes[tabConversation]
	d := v.detail
	if d == nil {
		p.lines, p.marks, v.sidebar = nil, nil, nil
		return
	}
	th := currentDiffTheme()
	w := v.mainWidth(tabConversation)
	cw := w - 2 // card width; every line gets a 1-col left margin
	on := func(band, fg string, bold bool, s string) string { return sty(fg, band, bold, false).Render(s) }

	var lines []string
	var marks []int
	add := func(ls ...string) {
		for _, l := range ls {
			lines = append(lines, " "+l)
		}
	}
	connector := sty(th.divider, "", false, false).Render(" │")

	if v.sidebarWidth() == 0 {
		v.sidebar = nil
		add(v.compactSidebar(cw), "")
	} else {
		v.sidebar = v.buildSidebar(v.sidebarWidth())
	}

	// GitHub shows the description as the author's opening comment.
	commentCard := func(author string, at time.Time, body string, isDescription bool) []string {
		band := th.band
		if author == d.Author.Login {
			band = th.authorBand
		}
		left := on(band, th.text, true, author) + on(band, th.muted, false, " commented "+relTime(at))
		right := ""
		if author == d.Author.Login {
			right = on(band, th.muted, false, "Author")
		}
		md := richBody(body, cw-4)
		if len(md) == 0 && isDescription {
			md = []string{sty(th.muted, "", false, true).Render("No description provided.")}
		}
		return bandCard(left, right, md, cw, th.border, band)
	}

	marks = append(marks, len(lines))
	add(commentCard(d.Author.Login, d.CreatedAt, d.Body, true)...)

	var items []timelineItem
	for _, c := range d.Comments.Nodes {
		c := c
		items = append(items, timelineItem{at: c.CreatedAt, render: func(int) []string {
			return commentCard(c.Author.Login, c.CreatedAt, c.Body, false)
		}})
	}
	for _, r := range d.Reviews.Nodes {
		r := r
		// Empty COMMENTED reviews only wrap inline threads, which appear on their own.
		if r.State == "PENDING" || (r.State == "COMMENTED" && cleanBody(r.Body) == "") {
			continue
		}
		icon, verb, color := reviewVerb(r.State, th)
		if cleanBody(r.Body) == "" {
			items = append(items, timelineItem{at: r.SubmittedAt, kind: itemEvent, render: func(int) []string {
				return []string{" " + sty(color, "", true, false).Render(icon) + "  " +
					sty(th.text, "", true, false).Render(r.Author.Login) + sty(th.muted, "", false, false).Render(" "+verb+" · "+relTime(r.SubmittedAt))}
			}})
			continue
		}
		items = append(items, timelineItem{at: r.SubmittedAt, render: func(int) []string {
			left := on(th.band, color, true, icon+" ") + on(th.band, th.text, true, r.Author.Login) +
				on(th.band, th.muted, false, " "+verb+" "+relTime(r.SubmittedAt))
			return bandCard(left, "", richBody(r.Body, cw-4), cw, color, th.band)
		}})
	}
	style := styles.Get(th.chroma)
	for _, t := range d.ReviewThreads.Nodes {
		t := t
		if len(t.Comments.Nodes) == 0 {
			continue
		}
		at := t.Comments.Nodes[0].CreatedAt
		if t.IsResolved {
			// GitHub folds resolved conversations; the Files tab still shows them in full.
			items = append(items, timelineItem{at: at, kind: itemEvent, render: func(w int) []string {
				n := len(t.Comments.Nodes)
				noun := "comments"
				if n == 1 {
					noun = "comment"
				}
				line := " " + approvedStyle.Render("✓") + "  " + sty(th.muted, "", false, false).Render("Resolved conversation on ") +
					pathStyle.Render(threadLoc(t)) + sty(th.muted, "", false, false).Render(fmt.Sprintf(" · %d %s", n, noun))
				return []string{ansi.Truncate(line, w, "…")}
			}})
			continue
		}
		items = append(items, timelineItem{at: at, render: func(int) []string {
			return conversationThreadCard(t, threadSnippet(t, style, cw-4, th), cw, th)
		}})
	}
	for _, c := range d.AllCommits.Nodes {
		items = append(items, timelineItem{at: c.Commit.CommittedDate, kind: itemCommit, commit: c})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })

	for i := 0; i < len(items); i++ {
		add(connector)
		marks = append(marks, len(lines))
		if items[i].kind != itemCommit {
			add(items[i].render(cw)...)
			continue
		}
		// Consecutive commits collapse into one "added N commits" event.
		j := i
		for j < len(items) && items[j].kind == itemCommit {
			j++
		}
		add(commitGroup(items[i:j], cw, th)...)
		i = j - 1
	}

	add(connector)
	marks = append(marks, len(lines))
	add(mergeBox(d, cw, th)...)
	p.lines, p.marks = lines, marks
}

func reviewVerb(state string, th diffTheme) (icon, verb, color string) {
	switch state {
	case "APPROVED":
		return "✓", "approved these changes", th.addBar
	case "CHANGES_REQUESTED":
		return "✗", "requested changes", th.delBar
	case "DISMISSED":
		return "●", "had a review dismissed", th.muted
	}
	return "●", "reviewed", th.muted
}

func commitGroup(items []timelineItem, width int, th diffTheme) []string {
	muted := sty(th.muted, "", false, false)
	author := items[0].commit.authorName()
	noun := "commits"
	if len(items) == 1 {
		noun = "commit"
	}
	out := []string{" " + muted.Render("○") + "  " + sty(th.text, "", true, false).Render(author) +
		muted.Render(fmt.Sprintf(" added %d %s · %s", len(items), noun, relTime(items[0].at)))}
	const shown = 5
	for i, it := range items {
		if i == shown {
			out = append(out, "    "+muted.Render(fmt.Sprintf("… %d more in Commits", len(items)-shown)))
			break
		}
		c := it.commit.Commit
		out = append(out, "    "+muted.Render(c.AbbreviatedOid)+"  "+
			sty(th.text, "", false, false).Render(truncate(c.MessageHeadline, max(width-16, 10))))
	}
	return out
}

func threadLoc(t reviewThread) string {
	switch {
	case t.Line != nil:
		return fmt.Sprintf("%s:%d", t.Path, *t.Line)
	case t.OriginalLine != nil:
		return fmt.Sprintf("%s:%d", t.Path, *t.OriginalLine)
	}
	return t.Path
}

// conversationThreadCard mirrors GitHub's review thread box: file header,
// code context, then each comment with its author.
func conversationThreadCard(t reviewThread, snippet []string, width int, th diffTheme) []string {
	left := sty(th.branchFg, th.band, false, false).Render(threadLoc(t))
	right := ""
	if t.IsOutdated {
		right = sty(th.muted, th.band, false, false).Render("Outdated")
	}
	body := append([]string{}, snippet...)
	for i, c := range t.Comments.Nodes {
		if i > 0 || len(snippet) > 0 {
			body = append(body, sty(th.divider, "", false, false).Render(strings.Repeat("┄", width-4)))
		}
		body = append(body, sty(th.text, "", true, false).Render(c.Author.Login)+sty(th.muted, "", false, false).Render(" "+relTime(c.CreatedAt)))
		body = append(body, richBody(c.Body, width-4)...)
	}
	return bandCard(left, right, body, width, th.threadBorder, th.band)
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

// mergeBox is the status summary GitHub shows at the end of the conversation.
func mergeBox(d *prDetail, width int, th diffTheme) []string {
	type row struct {
		ok           int // 1 good, -1 bad, 0 pending
		title, sub   string
		customColour string
	}
	var rows []row
	switch d.State {
	case "MERGED":
		rows = append(rows, row{ok: 1, title: "Pull request successfully merged and closed", customColour: "#8957e5"})
	case "CLOSED":
		rows = append(rows, row{ok: -1, title: "Closed with unmerged commits"})
	default:
		if d.IsDraft {
			rows = append(rows, row{title: "This pull request is still a draft", sub: "Mark it ready to notify reviewers and allow merging"})
		}
		latest, order := latestReviewStates(d)
		var approvers, blockers []string
		for _, login := range order {
			switch latest[login] {
			case "APPROVED":
				approvers = append(approvers, login)
			case "CHANGES_REQUESTED":
				blockers = append(blockers, login)
			}
		}
		switch {
		case d.ReviewDecision == "CHANGES_REQUESTED" || len(blockers) > 0:
			rows = append(rows, row{ok: -1, title: "Changes requested", sub: plural(len(blockers), "review") + " requesting changes by " + strings.Join(blockers, ", ")})
		case d.ReviewDecision == "APPROVED" || (d.ReviewDecision == "" && len(approvers) > 0):
			rows = append(rows, row{ok: 1, title: "Changes approved", sub: plural(len(approvers), "approving review") + " by " + strings.Join(approvers, ", ")})
		case d.ReviewDecision == "REVIEW_REQUIRED":
			rows = append(rows, row{title: "Review required", sub: "At least 1 approving review is required to merge"})
		default:
			rows = append(rows, row{title: "No reviews yet"})
		}

		if checks := checkContexts(d); checks != nil {
			passed, failed, pending := countChecks(checks)
			switch {
			case failed > 0:
				rows = append(rows, row{ok: -1, title: "Some checks were not successful", sub: fmt.Sprintf("%d failing, %d successful, %d pending", failed, passed, pending)})
			case pending > 0:
				rows = append(rows, row{title: "Some checks haven't completed yet", sub: fmt.Sprintf("%d pending, %d successful", pending, passed)})
			default:
				rows = append(rows, row{ok: 1, title: "All checks have passed", sub: plural(passed, "successful check")})
			}
		}

		switch d.Mergeable {
		case "MERGEABLE":
			rows = append(rows, row{ok: 1, title: "This branch has no conflicts with the base branch"})
		case "CONFLICTING":
			rows = append(rows, row{ok: -1, title: "This branch has conflicts that must be resolved"})
		default:
			rows = append(rows, row{title: "Checking for ability to merge…"})
		}
	}

	border := th.addBar
	for _, r := range rows {
		switch {
		case r.ok < 0:
			border = th.delBar
		case r.ok == 0 && border != th.delBar:
			border = th.threadBorder
		}
	}
	bs := sty(border, "", false, false)
	inner := width - 2
	out := []string{bs.Render("╭" + strings.Repeat("─", inner) + "╮")}
	for i, r := range rows {
		if i > 0 {
			out = append(out, bs.Render("├"+strings.Repeat("─", inner)+"┤"))
		}
		icon, color := "●", th.threadBorder
		switch {
		case r.customColour != "":
			icon, color = "✓", r.customColour
		case r.ok > 0:
			icon, color = "✓", th.addBar
		case r.ok < 0:
			icon, color = "✗", th.delBar
		}
		line := sty(color, "", true, false).Render(icon) + "  " + sty(th.text, "", true, false).Render(r.title)
		out = append(out, bs.Render("│")+" "+fitWidth(line, inner-2)+" "+bs.Render("│"))
		if r.sub != "" {
			out = append(out, bs.Render("│")+" "+fitWidth("   "+sty(th.muted, "", false, false).Render(r.sub), inner-2)+" "+bs.Render("│"))
		}
	}
	if d.State == "OPEN" {
		out = append(out, bs.Render("├"+strings.Repeat("─", inner)+"┤"))
		key, action := "m", " merge pull request"
		if d.IsDraft {
			key, action = "W", " mark ready for review"
		}
		hint := sty(th.addBar, "", true, false).Render(key) + sty(th.muted, "", false, false).Render(action)
		out = append(out, bs.Render("│")+" "+fitWidth(hint, inner-2)+" "+bs.Render("│"))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// buildSidebar renders GitHub's right column: reviewers, assignees, labels.
func (v *prDetailView) buildSidebar(width int) []string {
	d := v.detail
	th := currentDiffTheme()
	muted := sty(th.muted, "", false, false)
	heading := func(title, hint string) string {
		return rightAlign(sty(th.muted, "", true, false).Render(title), muted.Render(hint), width)
	}
	rule := sty(th.divider, "", false, false).Render(strings.Repeat("─", width))

	out := []string{heading("Reviewers", "r to edit")}
	latest, order := latestReviewStates(d)
	any := false
	for _, login := range order {
		icon, color := "●", th.muted
		switch latest[login] {
		case "APPROVED":
			icon, color = "✓", th.addBar
		case "CHANGES_REQUESTED":
			icon, color = "✗", th.delBar
		}
		out = append(out, rightAlign(sty(th.text, "", false, false).Render(login), sty(color, "", true, false).Render(icon), width))
		any = true
	}
	for _, name := range requestedReviewers(v.pr, d) {
		if _, reviewed := latest[name]; reviewed {
			continue
		}
		out = append(out, rightAlign(sty(th.text, "", false, false).Render(name), sty(th.threadBorder, "", false, false).Render("pending"), width))
		any = true
	}
	if !any {
		out = append(out, muted.Render("No reviews"))
	}

	out = append(out, rule, heading("Assignees", ""))
	if d == nil || len(d.Assignees.Nodes) == 0 {
		out = append(out, muted.Render("No one assigned"))
	} else {
		for _, a := range d.Assignees.Nodes {
			out = append(out, sty(th.text, "", false, false).Render(a.Login))
		}
	}

	out = append(out, rule, heading("Labels", ""))
	if d == nil || len(d.Labels.Nodes) == 0 {
		out = append(out, muted.Render("None yet"))
	} else {
		line := ""
		for _, l := range d.Labels.Nodes {
			c := labelChip(l)
			if line != "" && lipgloss.Width(line)+1+lipgloss.Width(c) > width {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += c
		}
		out = append(out, line)
	}
	return append(out, rule)
}

// compactSidebar is the sidebar as one line for narrow terminals: reviewer
// states, then labels.
func (v *prDetailView) compactSidebar(width int) string {
	th := currentDiffTheme()
	latest, order := latestReviewStates(v.detail)
	var parts []string
	for _, login := range order {
		icon, color := "●", th.muted
		switch latest[login] {
		case "APPROVED":
			icon, color = "✓", th.addBar
		case "CHANGES_REQUESTED":
			icon, color = "✗", th.delBar
		}
		parts = append(parts, sty(color, "", true, false).Render(icon)+" "+sty(th.text, "", false, false).Render(login))
	}
	for _, name := range requestedReviewers(v.pr, v.detail) {
		if _, reviewed := latest[name]; !reviewed {
			parts = append(parts, sty(th.threadBorder, "", false, false).Render("○ "+name))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, sty(th.muted, "", false, false).Render("No reviewers"))
	}
	if v.detail != nil {
		for _, l := range v.detail.Labels.Nodes {
			parts = append(parts, labelChip(l))
		}
	}
	return rightAlign(strings.Join(parts, "  "), sty(th.muted, "", false, false).Render("r to edit"), width)
}

func labelChip(l prLabel) string {
	bg := "#" + l.Color
	fg := "#ffffff"
	if r, g, b, ok := hexRGB(l.Color); ok && (r*299+g*587+b*114)/1000 > 150 {
		fg = "#1f2328"
	}
	return chip(l.Name, fg, bg)
}

func hexRGB(h string) (int, int, int, bool) {
	if len(h) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

// latestReviewStates maps each reviewer (not the author) to their latest
// meaningful review state; a later COMMENTED doesn't override approval.
func latestReviewStates(d *prDetail) (map[string]string, []string) {
	latest := map[string]string{}
	var order []string
	if d == nil {
		return latest, order
	}
	for _, r := range d.Reviews.Nodes {
		login := r.Author.Login
		if login == "" || login == d.Author.Login || r.State == "PENDING" {
			continue
		}
		if _, ok := latest[login]; !ok {
			order = append(order, login)
		}
		if prev := latest[login]; r.State != "COMMENTED" || prev == "" || prev == "COMMENTED" {
			latest[login] = r.State
		}
	}
	return latest, order
}

func requestedReviewers(pr PR, d *prDetail) []string {
	if d == nil {
		return strings.Fields(getRequestedReviewerNames(pr))
	}
	var names []string
	for _, rr := range d.ReviewRequests.Nodes {
		name := rr.RequestedReviewer.Login
		if name == "" {
			name = rr.RequestedReviewer.Name
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
