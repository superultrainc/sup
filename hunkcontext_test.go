package main

import (
	"reflect"
	"testing"
)

func thread(path, side string, start, line *int, resolved bool, bodies ...string) reviewThread {
	t := reviewThread{Path: path, DiffSide: side, StartLine: start, Line: line, IsResolved: resolved}
	for i, b := range bodies {
		t.Comments.Nodes = append(t.Comments.Nodes, prComment{Author: ghActor{[]string{"alice", "bob"}[i%2]}, Body: b})
	}
	return t
}

func intp(n int) *int { return &n }

func TestBuildHunkContext(t *testing.T) {
	d := &prDetail{Title: "T"}
	d.ReviewThreads.Nodes = []reviewThread{
		thread("a.go", "RIGHT", intp(10), intp(12), false, "range note", "reply"),
		thread("a.go", "LEFT", nil, intp(3), true, "old side"),
		thread("b.go", "RIGHT", nil, nil, false, "outdated, no line"),
		thread("c.go", "RIGHT", nil, intp(1), false, "<!-- hidden -->"),
	}
	ctx := buildHunkContext(d)

	if len(ctx.Files) != 2 {
		t.Fatalf("want 2 files (b.go skipped), got %d", len(ctx.Files))
	}
	a := ctx.Files[0].Annotations
	if len(a) != 2 {
		t.Fatalf("want 2 annotations on a.go, got %d", len(a))
	}
	if !reflect.DeepEqual(a[0].NewRange, []int{10, 12}) || a[0].OldRange != nil {
		t.Errorf("range: got new=%v old=%v", a[0].NewRange, a[0].OldRange)
	}
	if a[0].Summary != "range note" || a[0].Rationale != "@bob: reply" {
		t.Errorf("summary/rationale: %q / %q", a[0].Summary, a[0].Rationale)
	}
	if !reflect.DeepEqual(a[1].OldRange, []int{3, 3}) || a[1].Summary != "[resolved] old side" {
		t.Errorf("left side: %+v", a[1])
	}
	if c := ctx.Files[1].Annotations[0]; c.Summary != "(empty comment)" {
		t.Errorf("empty body should get a placeholder summary, got %q", c.Summary)
	}
}
