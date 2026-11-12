package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeKeys(m model, keys ...string) model {
	for _, k := range keys {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		if t, ok := map[string]tea.KeyType{"down": tea.KeyDown, "up": tea.KeyUp, "tab": tea.KeyTab, "enter": tea.KeyEnter, "esc": tea.KeyEsc}[k]; ok {
			msg = tea.KeyMsg{Type: t}
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func TestCommentMentionCompletion(t *testing.T) {
	pr := mockPRs()[0]
	m := model{width: 100, height: 40}
	m.comment = &commentDialog{action: "comment", pr: pr}
	m.comment.setPeople([]reviewerCandidate{{id: "zed", name: "Zed"}, {id: "mike", name: "Mike Chen"}, {id: "mia"}}, nil)

	m = typeKeys(m, "hi ", "@", "mi")
	if got := len(m.comment.matches); got != 2 {
		t.Fatalf("want 2 matches for @mi, got %d", got)
	}
	m = typeKeys(m, "down", "tab")
	if got := string(m.comment.buf); got != "hi @mia " {
		t.Fatalf("buf = %q", got)
	}
	// Emails aren't mentions.
	m = typeKeys(m, "a@m")
	if len(m.comment.matches) != 0 {
		t.Fatal("completed inside an email")
	}
	if !strings.Contains(m.commentView(), "Comment on PR") {
		t.Fatal("view missing title")
	}
}

func TestCommentDraftKeptOnEsc(t *testing.T) {
	pr := mockPRs()[0]
	m := model{width: 100, height: 40}
	m.openComment("comment", pr)
	m = typeKeys(m, "draft", "enter", "two")
	m = typeKeys(m, "esc")
	if m.comment != nil {
		t.Fatal("esc should close")
	}
	m.openComment("comment", pr)
	if got := string(m.comment.buf); got != "draft\ntwo" {
		t.Fatalf("draft = %q", got)
	}
	m = typeKeys(m, "up")
	if m.comment.pos != 3 {
		t.Fatalf("up kept column: pos = %d", m.comment.pos)
	}
}
