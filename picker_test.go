package main

import (
	"strings"
	"testing"
)

func TestReviewerChangeIsOptimistic(t *testing.T) {
	d := &prDetail{}
	applyReviewerChange(d, []string{"max", "superultrainc/backend"}, nil)
	got := requestedReviewers(PR{}, d)
	if len(got) != 2 || got[0] != "max" || got[1] != "backend" {
		t.Fatalf("after add: %v", got)
	}
	applyReviewerChange(d, nil, []string{"superultrainc/backend"})
	if got := requestedReviewers(PR{}, d); len(got) != 1 || got[0] != "max" {
		t.Fatalf("after removing the team: %v", got)
	}
}

func TestPickerOpensWithCachedPeople(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	pr := PR{}
	pr.Repository.Owner.Login = "acme"
	pr.Author.Login = "me"
	if p := newReviewerPicker(pr, nil); !p.loading {
		t.Fatal("no cache yet, so the picker should load")
	}
	savePeople("acme", []reviewerCandidate{{id: "max", name: "Max", suggested: true}, {id: "me"}, {id: "acme/web", team: true}})
	p := newReviewerPicker(pr, nil)
	if p.loading || !p.stale || len(p.cands) != 2 || p.cands[0].id != "max" || p.cands[0].suggested {
		t.Fatalf("want cached people without the author or suggestions, got %+v", p.cands)
	}
	p.setCandidates([]reviewerCandidate{{id: "max"}, {id: "ben"}}, "me", nil)
	if p.stale || len(p.cands) != 2 {
		t.Fatal("fresh people should replace the cache")
	}
}

func pickerModel(cands ...string) model {
	pr := PR{}
	pr.Repository.Owner.Login = "acme"
	m := model{width: 100, height: 40, cache: map[string]*prCacheEntry{}}
	m.detail = newDetailView(pr)
	m.detail.width = 100
	m.detail.detail = &prDetail{}
	p := &reviewerPicker{selected: map[string]bool{}, initial: map[string]bool{}}
	for _, id := range cands {
		p.cands = append(p.cands, reviewerCandidate{id: id})
	}
	m.detail.picker = p
	return m
}

func TestPickerEnterRequestsHighlighted(t *testing.T) {
	m := typeKeys(pickerModel("max", "ben"), "down", "enter")
	if m.detail.picker != nil {
		t.Fatal("enter should close the picker")
	}
	if m.actionStatus != "✓ Reviewers: requested ben" {
		t.Fatalf("status = %q", m.actionStatus)
	}
	if got := requestedReviewers(PR{}, m.detail.detail); len(got) != 1 || got[0] != "ben" {
		t.Fatalf("requested = %v", got)
	}
}

func TestPickerEnterSavesToggledOnly(t *testing.T) {
	m := typeKeys(pickerModel("max", "ben", "ana"), "tab", "down", "tab", "down", "enter")
	if m.actionStatus != "✓ Reviewers: requested ben, max" {
		t.Fatalf("toggled people should be saved, not the highlighted one: %q", m.actionStatus)
	}
}

func TestPickerEnterOnRequestedPersonCloses(t *testing.T) {
	m := pickerModel("max")
	m.detail.picker.initial["max"], m.detail.picker.selected["max"] = true, true
	m = typeKeys(m, "enter")
	if m.detail.picker != nil || m.actionStatus != "" {
		t.Fatalf("enter on an already requested person should just close, status %q", m.actionStatus)
	}
}

func TestPickerFooterFitsOneLine(t *testing.T) {
	for _, line := range strings.Split(pickerModel("max").pickerView(), "\n") {
		if strings.Contains(line, " esc ") && !strings.Contains(line, "type to filter") {
			t.Fatalf("footer wrapped: %q", line)
		}
	}
}
