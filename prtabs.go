package main

import (
	"fmt"
	"sort"
	"strings"
)

// checkContexts returns the head commit's checks, or nil when it has none.
func checkContexts(d *prDetail) []checkContext {
	if d == nil || len(d.Commits.Nodes) == 0 || d.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return nil
	}
	return d.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes
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

func countChecks(checks []checkContext) (passed, failed, pending int) {
	for _, c := range checks {
		switch checkState(c.result()) {
		case 1:
			passed++
		case -1:
			failed++
		default:
			pending++
		}
	}
	return
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

func checkResultLabel(result string) string {
	switch result {
	case "SUCCESS":
		return "Successful"
	case "SKIPPED":
		return "Skipped"
	case "NEUTRAL":
		return "Neutral"
	case "FAILURE", "ERROR":
		return "Failing"
	case "CANCELLED":
		return "Cancelled"
	case "TIMED_OUT":
		return "Timed out"
	case "IN_PROGRESS":
		return "In progress"
	case "QUEUED", "PENDING", "EXPECTED", "WAITING", "REQUESTED":
		return "Queued"
	}
	return strings.ToLower(result)
}

func (v *prDetailView) rebuildChecks() {
	p := &v.panes[tabChecks]
	if v.detail == nil {
		p.lines, p.marks = nil, nil
		return
	}
	th := currentDiffTheme()
	cw := v.mainWidth(tabChecks) - 2
	checks := checkContexts(v.detail)
	if len(checks) == 0 {
		p.lines, p.marks = []string{"", "  " + sty(th.muted, "", false, false).Render("No checks reported on the latest commit.")}, nil
		return
	}
	sorted := append([]checkContext{}, checks...)
	// Failures first, then running, then done; GitHub orders the same way.
	rank := map[int]int{-1: 0, 0: 1, 1: 2}
	sort.SliceStable(sorted, func(i, j int) bool {
		return rank[checkState(sorted[i].result())] < rank[checkState(sorted[j].result())]
	})

	passed, failed, pending := countChecks(checks)
	title, color := "All checks have passed", th.addBar
	switch {
	case failed > 0:
		title, color = "Some checks were not successful", th.delBar
	case pending > 0:
		title, color = "Some checks haven't completed yet", th.threadBorder
	}
	var parts []string
	for _, c := range []struct {
		n    int
		word string
	}{{failed, "failing"}, {pending, "pending"}, {passed, "successful"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.word))
		}
	}
	left := sty(color, th.band, true, false).Render("● ") + sty(th.text, th.band, true, false).Render(title)
	right := sty(th.muted, th.band, false, false).Render(strings.Join(parts, ", "))

	var body []string
	for _, c := range sorted {
		res := c.result()
		body = append(body, rightAlign(checkIcon(res)+"  "+sty(th.text, "", false, false).Render(c.displayName()),
			sty(th.muted, "", false, false).Render(checkResultLabel(res)), cw-4))
	}
	lines := []string{""}
	for _, l := range bandCard(left, right, body, cw, th.border, th.band) {
		lines = append(lines, " "+l)
	}
	p.lines, p.marks = lines, []int{0}
}

// rebuildCommits groups commits by day like GitHub's Commits tab.
func (v *prDetailView) rebuildCommits() {
	p := &v.panes[tabCommits]
	if v.detail == nil {
		p.lines, p.marks = nil, nil
		return
	}
	th := currentDiffTheme()
	cw := v.mainWidth(tabCommits) - 2
	commits := v.detail.AllCommits.Nodes
	if len(commits) == 0 {
		p.lines, p.marks = []string{"", "  " + sty(th.muted, "", false, false).Render("No commits loaded. Press R to reload.")}, nil
		return
	}
	muted := sty(th.muted, "", false, false)
	bs := sty(th.border, "", false, false)
	inner := cw - 2

	var lines []string
	var marks []int
	add := func(l string) { lines = append(lines, " "+l) }
	for i := 0; i < len(commits); {
		day := commits[i].Commit.CommittedDate.Local().Format("Jan 2, 2006")
		j := i
		for j < len(commits) && commits[j].Commit.CommittedDate.Local().Format("Jan 2, 2006") == day {
			j++
		}
		add("")
		marks = append(marks, len(lines))
		add(muted.Render("○  Commits on " + day))
		add(bs.Render("╭" + strings.Repeat("─", inner) + "╮"))
		for k := i; k < j; k++ {
			c := commits[k].Commit
			if k > i {
				add(bs.Render("├" + strings.Repeat("─", inner) + "┤"))
			}
			sha := chip(c.AbbreviatedOid, th.muted, th.chipBg)
			headline := sty(th.text, "", true, false).Render(truncate(c.MessageHeadline, max(inner-14, 10)))
			add(bs.Render("│") + " " + rightAlign(headline, sha, inner-2) + " " + bs.Render("│"))
			meta := sty(th.text, "", false, false).Render(commits[k].authorName()) + muted.Render(" committed "+relTime(c.CommittedDate))
			if c.StatusCheckRollup != nil {
				meta += "  " + checkIcon(c.StatusCheckRollup.State)
			}
			add(bs.Render("│") + " " + fitWidth(meta, inner-2) + " " + bs.Render("│"))
		}
		add(bs.Render("╰" + strings.Repeat("─", inner) + "╯"))
		i = j
	}
	p.lines, p.marks = lines, marks
}
