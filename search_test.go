package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func TestDetailSearch(t *testing.T) {
	m := detailModel(120, 30)
	m.detail.tab = tabFiles
	m = press(t, m, "/", "N", "e", "w", "T", "h", "i", "n", "g", "enter")

	v := m.detail
	if v.search == nil || v.search.typing || v.search.query != "NewThing" {
		t.Fatalf("search not committed: %+v", v.search)
	}
	if len(v.search.matches) < 2 {
		t.Fatalf("want several matches for NewThing, got %d", len(v.search.matches))
	}
	line := ansi.Strip(v.pane().lines[v.currentMatchLine()])
	if !strings.Contains(strings.ToLower(line), "newthing") {
		t.Fatalf("current match line doesn't contain the query: %q", line)
	}

	first := v.search.current
	m = press(t, m, "n")
	if m.detail.search.current != (first+1)%len(v.search.matches) {
		t.Errorf("n should advance to the next match")
	}
	m = press(t, m, "N")
	if m.detail.search.current != first {
		t.Errorf("N should go back to the previous match")
	}

	// First esc clears the search, the second leaves the view.
	m = press(t, m, "esc")
	if m.detail == nil || m.detail.search != nil {
		t.Fatalf("esc should clear the search but stay in the view")
	}
	m = press(t, m, "esc")
	if m.detail != nil {
		t.Fatalf("second esc should leave the view")
	}
}
