package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// chip renders a small filled label, like GitHub's counters and branch names.
func chip(text, fg, bg string) string {
	return sty(fg, bg, false, false).Render(" " + text + " ")
}

// diffBoxes is GitHub's five-square additions/deletions bar.
func diffBoxes(adds, dels int, th diffTheme) string {
	total := adds + dels
	if total == 0 {
		return sty(th.muted, "", false, false).Render("■■■■■")
	}
	green := (adds*5 + total/2) / total
	return sty(th.addBar, "", false, false).Render(strings.Repeat("■", green)) +
		sty(th.delBar, "", false, false).Render(strings.Repeat("■", 5-green))
}

// statePill is the PR's state badge: Open, Draft, Merged, or Closed.
func statePill(pr PR, d *prDetail) string {
	state, draft := "OPEN", pr.IsDraft
	if d != nil {
		state, draft = d.State, d.IsDraft
	}
	label, bg := "Open", "#238636"
	switch {
	case state == "MERGED":
		label, bg = "Merged", "#8957e5"
	case state == "CLOSED":
		label, bg = "Closed", "#da3633"
	case draft:
		label, bg = "Draft", "#6e7681"
	}
	return sty("#ffffff", bg, true, false).Render(" " + label + " ")
}

// rightAlign places right at the end of a width-wide line, dropping it if it won't fit.
func rightAlign(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return fitWidth(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

var tabNames = [tabCount]string{
	tabConversation: "Conversation",
	tabFiles:        "Files changed",
	tabCommits:      "Commits",
	tabChecks:       "Checks",
}

func (v *prDetailView) tabCounts() [tabCount]int {
	var c [tabCount]int
	if d := v.detail; d != nil {
		c[tabConversation] = len(d.Comments.Nodes) + len(d.ReviewThreads.Nodes)
		for _, r := range d.Reviews.Nodes {
			if cleanBody(r.Body) != "" {
				c[tabConversation]++
			}
		}
		c[tabCommits] = d.Commits.TotalCount
		if checks := checkContexts(d); checks != nil {
			c[tabChecks] = len(checks)
		}
		c[tabFiles] = d.ChangedFiles
	}
	if v.diff != nil {
		c[tabFiles] = len(v.diff)
	}
	return c
}

func (m model) detailHeader() []string {
	v := m.detail
	d := v.detail
	th := currentDiffTheme()
	w := m.width - 1
	text := func(s string) string { return sty(th.text, "", false, false).Render(s) }
	muted := func(s string) string { return sty(th.muted, "", false, false).Render(s) }

	title, author, head, base := v.pr.Title, v.pr.Author.Login, v.pr.HeadRefName, ""
	adds, dels, commits := v.pr.Additions, v.pr.Deletions, 0
	if d != nil {
		title, author, head, base = d.Title, d.Author.Login, d.HeadRefName, d.BaseRefName
		adds, dels, commits = d.Additions, d.Deletions, d.Commits.TotalCount
	}
	slug := muted(v.pr.Repository.Owner.Login + "/" + v.pr.Repository.Name)
	num := muted(fmt.Sprintf("#%d", v.pr.Number))
	titleW := w - 2 - lipgloss.Width(num) - lipgloss.Width(slug) - 3
	line1 := rightAlign(" "+prTitleStyle.Render(truncate(title, max(titleW, 10)))+" "+num, slug, w)

	branch := func(name string) string { return chip(name, th.branchFg, th.branchBg) }
	verb := " wants to merge "
	if d != nil && d.State == "MERGED" {
		verb = " merged "
	}
	what := "commits"
	if commits == 1 {
		what = "commit"
	}
	line2 := " " + statePill(v.pr, d) + "  " + sty(th.text, "", true, false).Render(author) + muted(verb)
	if d != nil {
		line2 += text(fmt.Sprintf("%d %s", commits, what)) + muted(" into ") + branch(base) + muted(" from ") + branch(head)
	} else {
		line2 += muted("from ") + branch(head)
	}
	line2 = fitWidth(line2, w)

	// Tabs, with the active one underlined in the rule below like GitHub.
	counts := v.tabCounts()
	tabs, rule := " ", " "
	for i, name := range tabNames {
		label := muted(name)
		if i == v.tab {
			label = sty(th.text, "", true, false).Render(name)
		}
		seg := label + " " + chip(fmt.Sprint(counts[i]), th.text, th.chipBg)
		segW := lipgloss.Width(seg)
		ruleChar, ruleColor := "─", th.divider
		if i == v.tab {
			ruleChar, ruleColor = "━", th.accent
		}
		tabs += seg
		rule += sty(ruleColor, "", false, false).Render(strings.Repeat(ruleChar, segW))
		if i < tabCount-1 {
			tabs += "   "
			rule += sty(th.divider, "", false, false).Render("───")
		}
	}
	stats := additionsStyle.Render("+"+commas(adds)) + " " + deletionsStyle.Render("−"+commas(dels)) + " " + diffBoxes(adds, dels, th)
	tabs = rightAlign(tabs, stats+" ", w)
	if rw := lipgloss.Width(rule); rw < w {
		rule += sty(th.divider, "", false, false).Render(strings.Repeat("─", w-rw))
	}
	return []string{line1, line2, tabs, rule}
}

func (m model) detailView() string {
	v := m.detail
	var s strings.Builder
	for _, l := range m.detailHeader() {
		s.WriteString(l + "\n")
	}

	bodyH := v.bodyHeight(m.height)
	var rows []string
	switch {
	case v.picker != nil:
		rows = m.pickerView(bodyH)
	case v.tab != tabFiles && v.detail == nil:
		rows = []string{"", m.placeholder(v.err, "Loading PR...")}
	case v.tab == tabFiles && v.diff == nil:
		rows = []string{"", m.placeholder(v.diffErr, "Loading diff...")}
	default:
		rows = m.paneRows(bodyH)
	}
	for i := 0; i < bodyH; i++ {
		if i < len(rows) {
			s.WriteString(rows[i])
		}
		s.WriteString("\n")
	}
	s.WriteString(m.detailFooter())
	return s.String()
}

func (m model) placeholder(err error, loading string) string {
	if err != nil {
		return changesRequestedStyle.Render("  Error: " + err.Error())
	}
	return reviewRequestedStyle.Render("  " + spinnerFrames[m.spinnerFrame] + " " + loading)
}

// paneRows composes the visible slice of the active pane with its side column.
func (m model) paneRows(height int) []string {
	v := m.detail
	p := v.pane()
	end := min(p.scroll+height, len(p.lines))
	visible := p.lines[p.scroll:end]

	var side []string
	sideW := 0
	switch {
	case v.tab == tabConversation && v.sidebarWidth() > 0:
		side, sideW = v.sidebar, v.sidebarWidth()
	case v.tab == tabFiles && v.treeWidth() > 0:
		sideW = v.treeWidth()
		side = fileTree(v.diff, v.currentFile(), v.detail, sideW, height)
	}
	if sideW == 0 {
		return visible
	}

	mainW := v.mainWidth(v.tab)
	rows := make([]string, height)
	for i := range rows {
		left := ""
		if i < len(visible) {
			left = visible[i]
		}
		right := ""
		if i < len(side) {
			right = side[i]
		}
		if v.tab == tabFiles {
			rows[i] = fitWidth(right, sideW) + " " + left
		} else {
			rows[i] = fitWidth(left, mainW) + " " + right
		}
	}
	return rows
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
	case (v.loading && v.detail != nil) || (v.diffLoading && v.diff != nil):
		status = reviewRequestedStyle.Render(" " + spinnerFrames[m.spinnerFrame] + " Refreshing...")
	}

	pct := "all"
	if ms := v.maxScroll(m.height); ms > 0 {
		pct = fmt.Sprintf("%d%%", v.pane().scroll*100/ms)
	}
	var help string
	switch {
	case v.picker != nil:
		help = " ↑/↓ move · space toggle · type to filter · enter save · esc cancel"
	case v.tab == tabFiles:
		help = " j/k scroll · [/] file · n/N comment · s layout · w wrap · x collapse · tab next tab · esc back"
	default:
		help = " j/k scroll · [/] next item · tab next tab · r reviewers · d hunk · A approve · D changes · M comment · o open · esc back"
	}
	return status + "\n" + helpStyle.Render(truncateToWidth(help+"  "+pct, max(m.width-1, 20)))
}
