package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUpdateBranchPinsHead(t *testing.T) {
	ghToken = "test"
	t.Cleanup(func() { ghToken = "" })
	reqs := fakeGitHub(t, map[string]string{"GET /repos/superultrainc/sup/pulls/7": "200 " + `{"head":{"sha":"abc123"}}`})
	if code := cliUpdateBranchCommand([]string{"superultrainc/sup#7"}); code != exitOK {
		t.Fatalf("got exit %d", code)
	}
	put := (*reqs)[1]
	if put.method != "PUT" || put.path != "/repos/superultrainc/sup/pulls/7/update-branch" || put.body["expected_head_sha"] != "abc123" {
		t.Fatalf("unexpected request: %+v", put)
	}
}

func TestUpdateBranchKey(t *testing.T) {
	reqs := fakeGitHub(t, nil)
	m := sampleModel()
	m.filtered[0].Number, m.filtered[0].Repository.Name, m.filtered[0].HeadRefOid = 7, "sup", "def456"
	m.visibleCount = len(m.filtered)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	if cmd == nil {
		t.Fatal("U should update the branch")
	}
	next, _ := m.Update(cmd())
	if got := next.(model).actionStatus; got != "✓ Updating #7 with its base branch" {
		t.Fatalf("got status %q", got)
	}
	if r := (*reqs)[0]; r.path != "/repos/superultrainc/sup/pulls/7/update-branch" || r.body["expected_head_sha"] != "def456" {
		t.Fatalf("unexpected request: %+v", r)
	}
}
