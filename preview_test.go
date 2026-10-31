package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func previewModel(t *testing.T, width int) model {
	m := initialModel()
	m.width, m.height = width, 30
	m.prs = mockPRs()
	m.applyFilter()
	m.visibleCount = len(m.filtered)
	m.loading = false
	for _, pr := range m.filtered[:2] {
		m.cacheEntry(prKey(pr)).detail = mockPRDetail(pr)
	}
	return press(t, m, " ")
}

func TestPreviewFollowsCursor(t *testing.T) {
	m := previewModel(t, 150)
	if !m.previewActive() || m.preview == nil || prKey(m.preview.pr) != prKey(m.filtered[0]) {
		t.Fatalf("space should preview the cursor's PR")
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Conversation") || strings.Contains(out, "BRANCH") {
		t.Fatalf("split view should show the compact list and the PR's conversation:\n%s", out)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = next.(model)
	if m.preview.panes[tabConversation].scroll == 0 {
		t.Fatalf("ctrl+d should scroll the preview")
	}

	m = press(t, m, "j")
	if prKey(m.preview.pr) != prKey(m.filtered[1]) || m.preview.panes[tabConversation].scroll != 0 {
		t.Fatalf("moving the cursor should preview the next PR from the top")
	}

	m = press(t, m, " ")
	if m.previewActive() || strings.Contains(ansi.Strip(m.View()), "Conversation") {
		t.Fatalf("space again should close the preview")
	}
}

func TestPreviewNeedsWidth(t *testing.T) {
	if m := previewModel(t, 90); m.previewActive() {
		t.Fatalf("narrow terminals should keep the full list")
	}
}
