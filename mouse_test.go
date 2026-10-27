package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func detailModel(width, height int) model {
	pr := mockPRs()[2]
	m := initialModel()
	m.width, m.height = width, height
	m.detail = newDetailView(pr)
	m.detail.detail = mockPRDetail(pr)
	m.detail.diff = parseUnifiedDiff(mockPatch)
	m.detail.loading, m.detail.diffLoading = false, false
	m.detail.rebuild(width)
	return m
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func TestClickTabs(t *testing.T) {
	m := detailModel(170, 40)
	header := ansi.Strip(m.detailHeader()[2])
	for i, span := range m.detail.tabSpans() {
		if got := header[span[0] : span[0]+len(tabNames[i])]; got != tabNames[i] {
			t.Fatalf("tab %d span %v covers %q, want %q", i, span, got, tabNames[i])
		}
		next, _ := m.handleMouse(click(span[0]+1, 2))
		if tab := next.(model).detail.tab; tab != i {
			t.Errorf("clicking %q selected tab %d", tabNames[i], tab)
		}
	}
}

func TestClickFileTree(t *testing.T) {
	m := detailModel(170, 40)
	m.detail.tab = tabFiles
	_, fileAt := fileTree(m.detail.diff, m.detail.currentFile(), m.detail.detail, m.detail.treeWidth(), m.detail.bodyHeight(m.height))
	for row, file := range fileAt {
		if file < 0 {
			continue
		}
		next, _ := m.handleMouse(click(5, detailHeaderHeight+row))
		if got := next.(model).detail.currentFile(); got != file {
			t.Errorf("clicking tree row %d should show file %d, got %d", row, file, got)
		}
	}
}
