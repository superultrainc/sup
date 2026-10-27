package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func listViewModel(n, width int) model {
	decisions := []string{"", "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"}
	var prs []PR
	for i := 0; i < n; i++ {
		pr := testPR(fmt.Sprintf("repo-%d", i%5), fmt.Sprintf("author%d", i), 100+i, i%7 == 0, decisions[i%4], "reviewer-with-long-name")
		pr.Title = fmt.Sprintf("Fix thing %d", i)
		if i%3 == 0 {
			pr.Title += " with a rather long title that gets truncated at this width"
		}
		pr.HeadRefName = fmt.Sprintf("feature/branch-%d", i)
		pr.Additions = i * 13
		pr.Deletions = i * 7
		if i%4 == 3 && i%2 == 1 {
			_ = json.Unmarshal([]byte(`{"nodes":[{"state":"COMMENTED"}]}`), &pr.Reviews)
		}
		prs = append(prs, pr)
	}
	m := model{prs: prs, filtered: prs, statusFilterIndex: -1, repoFilterIndex: -1, repos: buildRepoList(prs)}
	m.width, m.height, m.visibleCount, m.cursor = width, 40, n, 2
	return m
}

func withTrueColor(t *testing.T) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	styleCache = map[styleKey]painter{}
	t.Cleanup(func() {
		lipgloss.SetColorProfile(old)
		styleCache = map[styleKey]painter{}
	})
}

func TestListViewStrippedGolden(t *testing.T) {
	withTrueColor(t)
	out := listViewModel(20, 160).view()
	if p := os.Getenv("LISTVIEW_RAW_OUT"); p != "" {
		_ = os.WriteFile(p, []byte(out), 0o644)
	}
	got := ansi.Strip(out)
	const path = "testdata/list_view_stripped.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	if len(gl) != len(wl) {
		t.Fatalf("line count %d, want %d", len(gl), len(wl))
	}
	for i := range wl {
		if gl[i] != wl[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, gl[i], wl[i])
		}
	}
}

func TestListViewFitsWidthAndShowsRows(t *testing.T) {
	withTrueColor(t)
	const width = 160
	m := listViewModel(20, width)
	lines := strings.Split(m.view(), "\n")
	for i, line := range lines {
		// The separator rule is width-2+2 cells wide by design; rows are narrower.
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d width %d > %d", i, w, width)
		}
	}
	for _, pr := range m.filtered {
		num := fmt.Sprintf("#%d ", pr.Number)
		var row string
		for _, l := range lines {
			if s := ansi.Strip(l); strings.Contains(s, num) {
				row = s
			}
		}
		if row == "" {
			t.Fatalf("no row for PR %d", pr.Number)
		}
		title := pr.Title
		if len(title) > 30 {
			title = title[:20]
		}
		if !strings.Contains(row, title) {
			t.Errorf("PR %d row missing title %q: %q", pr.Number, title, row)
		}
		if label := statusBadgeLabel(pr); !strings.Contains(row, label) {
			t.Errorf("PR %d row missing %s: %q", pr.Number, label, row)
		}
	}
}
