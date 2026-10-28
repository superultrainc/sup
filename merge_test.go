package main

import "testing"

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
	if !isDraftPR(d) {
		t.Fatal("open draft should offer ready for review")
	}
	d.State = "CLOSED"
	if isDraftPR(d) {
		t.Fatal("closed draft can't be marked ready")
	}
}
