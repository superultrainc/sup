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

func TestDetailHelp(t *testing.T) {
	m := press(t, detailModel(120, 30), "?")
	if !m.helpMode || !strings.Contains(ansi.Strip(m.View()), "Collapse / expand file") {
		t.Fatalf("? should show the PR view shortcuts")
	}
	m = press(t, m, "j", "esc")
	if m.helpMode || m.detail == nil || m.detail.pane().scroll != 0 {
		t.Fatalf("help should swallow keys and esc should return to the PR view")
	}
}

func TestCommandPalette(t *testing.T) {
	m := detailModel(120, 30)
	m.detail.tab = tabFiles
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = next.(model)
	for cmd != nil {
		next, cmd = m.Update(cmd())
		m = next.(model)
	}
	if m.palette == nil || !strings.Contains(ansi.Strip(m.View()), "Edit reviewers") {
		t.Fatalf("ctrl+k should open the palette with PR view commands")
	}
	m = press(t, m, "w", "r", "a", "p")
	if cmds := m.paletteCommands(); len(cmds) != 1 || cmds[0].key != "w" {
		t.Fatalf("query should narrow to the wrap command, got %+v", cmds)
	}
	wrap := m.detail.wrap
	m = press(t, m, "enter")
	if m.palette != nil || m.detail.wrap == wrap {
		t.Fatalf("enter should close the palette and run the command")
	}
}

func TestCtrlUClearsInputs(t *testing.T) {
	ctrlU := tea.KeyMsg{Type: tea.KeyCtrlU}
	m := press(t, initialModel(), "/", "a", "b")
	next, _ := m.Update(ctrlU)
	if m = next.(model); m.filterText != "" || !m.filterMode {
		t.Fatalf("ctrl+u should clear the filter and keep it open, got %q", m.filterText)
	}

	m = press(t, detailModel(120, 30), "/", "a", "b")
	next, _ = m.Update(ctrlU)
	if m = next.(model); m.detail.search == nil || m.detail.search.query != "" {
		t.Fatalf("ctrl+u should clear the search query")
	}

	m = detailModel(120, 30)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = press(t, next.(model), "a", "b")
	next, _ = m.Update(ctrlU)
	if m = next.(model); m.palette == nil || m.palette.query != "" {
		t.Fatalf("ctrl+u should clear the palette query")
	}
}
