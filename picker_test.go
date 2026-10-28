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
