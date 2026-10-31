package main

import (
	"strings"
	"sync"
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

func TestPreviewLoadsImages(t *testing.T) {
	m := previewModel(t, 150)
	d := m.cacheEntry(prKey(m.filtered[0])).detail
	d.Body = "![shot](https://example.com/preview-test.png)"
	t.Cleanup(func() { delete(imageStore, "https://example.com/preview-test.png") })
	if cmd := m.handleHover(hoverMsg{seq: m.hoverSeq, pr: m.filtered[0]}); cmd == nil {
		t.Fatalf("resting on a PR in preview should load its images")
	}
	if e := imageStore["https://example.com/preview-test.png"]; e == nil || !e.loading {
		t.Fatalf("image should be queued, got %+v", e)
	}
}

func TestDemoNeverLoads(t *testing.T) {
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	m := initialModel()
	m.width, m.height = 150, 30
	m.handleRowsRefreshed(prsRefreshedMsg{prs: mockPRs()})
	m.loading, m.visibleCount = false, len(m.filtered)
	m = press(t, m, " ")
	for i := range m.filtered {
		if out := ansi.Strip(m.View()); strings.Contains(out, "Loading") {
			t.Fatalf("demo PR #%d shows a loading state", m.filtered[i].Number)
		}
		m = press(t, m, "j")
	}
	if len(m.filtered) < 40 {
		t.Fatalf("demo should have plenty of PRs, got %d", len(m.filtered))
	}
}

func TestDemoApprovalSticks(t *testing.T) {
	demoMode, currentUser = true, "sarah"
	t.Cleanup(func() { demoMode, currentUser = false, ""; demoActions = sync.Map{} })
	m := initialModel()
	m.width, m.height = 150, 30
	m.handleRowsRefreshed(prsRefreshedMsg{prs: mockPRs()})
	m.loading, m.visibleCount = false, len(m.filtered)
	for statusLabelForFilter(m.filtered[m.cursor]) != "review" {
		m.cursor++
	}
	key := prKey(m.filtered[m.cursor])
	m = press(t, press(t, m, " "), "A")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	if !strings.Contains(ansi.Strip(m.View()), "✓ Approved PR") {
		t.Fatalf("approving should show a confirmation banner")
	}
	// Run the submit, then the refetches it triggers.
	pending := []tea.Cmd{cmd}
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			pending = append(pending, msg...)
		case nil:
		default:
			next, more := m.Update(msg)
			m = next.(model)
			if _, isTick := msg.(kittyFlushedMsg); !isTick {
				pending = append(pending, more)
			}
		}
	}
	for _, pr := range m.prs {
		if prKey(pr) == key && statusLabelForFilter(pr) != "approved" {
			t.Fatalf("approved PR should show as approved, got %s", statusLabelForFilter(pr))
		}
	}
	if m.preview == nil || m.preview.detail.ReviewDecision != "APPROVED" {
		t.Fatalf("preview should refetch and show the approval")
	}
}
