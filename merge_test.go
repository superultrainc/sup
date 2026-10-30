package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestMergeDialogRules(t *testing.T) {
	d := &prDetail{State: "OPEN", MergeStateStatus: "CLEAN"}
	d.Repository.MergeCommitAllowed, d.Repository.RebaseMergeAllowed = true, true
	md := newMergeDialog(d)

	// Squash isn't allowed here, so the default falls back to the first allowed method.
	if m, ok := md.selectedMethod(d); !ok || m.id != "MERGE" {
		t.Fatalf("want MERGE, got %v %v", m.id, ok)
	}
	if r := md.blockReason(d); r != "" {
		t.Fatalf("clean PR should merge, got %q", r)
	}

	d.MergeStateStatus = "BLOCKED"
	md.auto = false
	if md.blockReason(d) == "" {
		t.Fatal("blocked PR without auto-merge should be refused")
	}
	md.auto = true
	if r := md.blockReason(d); r != "" {
		t.Fatalf("auto-merge on a blocked PR should be allowed, got %q", r)
	}

	d.MergeStateStatus = "DIRTY"
	if md.blockReason(d) == "" {
		t.Fatal("conflicting PR should be refused")
	}

	d.MergeStateStatus, d.State = "CLEAN", "MERGED"
	if md.blockReason(d) == "" {
		t.Fatal("already merged PR should be refused")
	}

	d.State, d.IsDraft = "OPEN", true
	if md.blockReason(d) == "" {
		t.Fatal("draft PR should be refused")
	}
}

func TestClosePR(t *testing.T) {
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	m := detailModel(120, 30)
	key := prKey(m.detail.pr)
	m = press(t, m, "X")
	if !strings.Contains(ansi.Strip(m.View()), "Close PR #") {
		t.Fatalf("X should ask to confirm closing")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	for _, pr := range m.prs {
		if prKey(pr) == key {
			t.Fatalf("closed PR should leave the list")
		}
	}
	if !strings.HasPrefix(m.actionStatus, "✓ Closed") {
		t.Fatalf("got status %q", m.actionStatus)
	}
}

func TestConvertToDraft(t *testing.T) {
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	m := detailModel(120, 30)
	m.detail.pr.IsDraft = false
	key := prKey(m.detail.pr)
	m = press(t, m, "W")
	if !strings.Contains(ansi.Strip(m.View()), "to draft? (y/n)") {
		t.Fatalf("W should ask to confirm")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !m.detail.pr.IsDraft || !strings.HasPrefix(m.actionStatus, "✓ Converted") {
		t.Fatalf("PR should be a draft, got status %q", m.actionStatus)
	}
	for _, pr := range m.prs {
		if prKey(pr) == key && !pr.IsDraft {
			t.Fatalf("list row should be a draft too")
		}
	}
}

func TestMarkReady(t *testing.T) {
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	m := detailModel(120, 30)
	m.detail.pr.IsDraft = true
	m = press(t, m, "W")
	if !strings.Contains(ansi.Strip(m.View()), "ready for review? (y/n)") {
		t.Fatalf("W on a draft should ask to mark it ready")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.detail.pr.IsDraft || !strings.HasPrefix(m.actionStatus, "✓ Marked") {
		t.Fatalf("PR should be ready, got status %q", m.actionStatus)
	}
}
