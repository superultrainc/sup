package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// detailSearch is the PR view's `/` search over the active tab's lines.
type detailSearch struct {
	typing  bool
	query   string
	origin  int   // scroll when `/` was pressed, restored on cancel
	matches []int // line indices in the active pane
	current int   // index into matches, -1 when none
}

// searchContext keeps a little room above a match so it isn't pinned to the top edge.
const searchContext = 2

// refreshSearch recomputes matches for the active tab; call after any rebuild or tab change.
func (v *prDetailView) refreshSearch() {
	s := v.search
	if s == nil || s.query == "" {
		return
	}
	q := strings.ToLower(s.query)
	s.matches = s.matches[:0]
	for i, l := range v.pane().plainLines() {
		if strings.Contains(l, q) {
			s.matches = append(s.matches, i)
		}
	}
	s.current = -1
}

// jumpToMatch moves to the first match at or after from (dir 1) or before it (dir -1), wrapping.
func (v *prDetailView) jumpToMatch(from, dir int, termHeight int) {
	s := v.search
	if s == nil || len(s.matches) == 0 {
		return
	}
	pick := -1
	if dir > 0 {
		for i, ln := range s.matches {
			if ln >= from {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = 0
		}
	} else {
		for i := len(s.matches) - 1; i >= 0; i-- {
			if s.matches[i] < from {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = len(s.matches) - 1
		}
	}
	s.current = pick
	p := v.pane()
	p.scroll = s.matches[pick] - searchContext
	v.clampScroll(termHeight)
}

// stepMatch moves to the next (dir 1) or previous match, wrapping; with no
// current match it starts from the top of the screen.
func (v *prDetailView) stepMatch(dir, termHeight int) {
	s := v.search
	if s == nil || len(s.matches) == 0 {
		return
	}
	if s.current < 0 {
		v.jumpToMatch(v.pane().scroll, dir, termHeight)
		return
	}
	s.current = (s.current + dir + len(s.matches)) % len(s.matches)
	v.pane().scroll = s.matches[s.current] - searchContext
	v.clampScroll(termHeight)
}

// currentMatchLine is the pane line to mark, or -1.
func (v *prDetailView) currentMatchLine() int {
	s := v.search
	if s == nil || s.current < 0 || s.current >= len(s.matches) {
		return -1
	}
	return s.matches[s.current]
}

func (m model) handleSearchInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.detail
	s := v.search
	switch msg.Type {
	case tea.KeyEsc:
		v.pane().scroll = s.origin
		v.search = nil
		return m, nil
	case tea.KeyEnter:
		s.typing = false
		if s.query == "" {
			v.search = nil
		}
		return m, nil
	case tea.KeyCtrlU: // cmd+backspace in most macOS terminals
		s.query = ""
	case tea.KeyBackspace:
		if s.query != "" {
			r := []rune(s.query)
			s.query = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		if len(msg.Runes) > 0 {
			s.query += string(msg.Runes)
		} else {
			s.query += " "
		}
	default:
		return m, nil
	}
	// Incremental: jump to the first match from where the search started.
	v.refreshSearch()
	if s.query == "" {
		v.pane().scroll = s.origin
	} else {
		v.jumpToMatch(s.origin, 1, m.height)
	}
	return m, nil
}

// searchStatus is the footer text for an active or in-progress search.
func (v *prDetailView) searchStatus() string {
	s := v.search
	th := currentDiffTheme()
	count := "no matches"
	if n := len(s.matches); n > 0 {
		pos := "-"
		if s.current >= 0 {
			pos = fmt.Sprint(s.current + 1)
		}
		count = fmt.Sprintf("%s/%d", pos, n)
	}
	if s.typing {
		return filterStyle.Render(" /"+s.query+"█") + sty(th.muted, "", false, false).Render("  "+count)
	}
	return filterStyle.Render(" /"+s.query) + sty(th.muted, "", false, false).Render("  "+count+" · n/N next · esc clear")
}
