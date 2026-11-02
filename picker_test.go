package main

import "testing"

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
