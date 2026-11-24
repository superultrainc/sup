package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func guardedPR() *prDetail {
	d := &prDetail{State: "OPEN", HeadRefOid: "abc1234def", Additions: 100, Deletions: 50, ChangedFiles: 4}
	d.Repository.SquashMergeAllowed = true
	review := func(login, state, oid string, at int) prReview {
		r := prReview{Author: ghActor{login}, State: state, SubmittedAt: time.Unix(int64(at), 0)}
		r.Commit.Oid = oid
		return r
	}
	d.Reviews.Nodes = []prReview{
		review("greptile-apps", "CHANGES_REQUESTED", "old", 1),
		review("greptile-apps", "APPROVED", "abc1234def", 2),
		review("greptile-apps", "COMMENTED", "abc1234def", 3), // a later comment keeps the approval
	}
	return d
}

func TestMergeGuards(t *testing.T) {
	squash := allMergeMethods[0]
	ok := mergeGuards{requireApproval: "greptile-apps[bot]", maxLines: 300, maxFiles: 10, matchHead: "abc1234"}
	if why := mergeRefusal(guardedPR(), squash, ok); why != "" {
		t.Fatalf("should merge, got %q", why)
	}
	if why := mergeRefusal(guardedPR(), squash, mergeGuards{maxLines: -1, maxFiles: -1}); why != "" {
		t.Fatalf("no guards should merge, got %q", why)
	}
	cases := []struct {
		name string
		edit func(d *prDetail, g *mergeGuards)
		want string
	}{
		{"too many lines", func(d *prDetail, g *mergeGuards) { g.maxLines = 149 }, "--max-lines"},
		{"too many files", func(d *prDetail, g *mergeGuards) { g.maxFiles = 3 }, "--max-files"},
		{"zero is a limit", func(d *prDetail, g *mergeGuards) { g.maxFiles = 0 }, "--max-files 0"},
		{"wrong head", func(d *prDetail, g *mergeGuards) { g.matchHead = "fff1234" }, "head is"},
		{"short head", func(d *prDetail, g *mergeGuards) { g.matchHead = "abc" }, "head is"},
		{"other reviewer", func(d *prDetail, g *mergeGuards) { g.requireApproval = "max" }, "hasn't approved"},
		{"approved older head", func(d *prDetail, g *mergeGuards) { d.HeadRefOid = "abc1234fff" }, "approved"},
		{"approval dismissed", func(d *prDetail, g *mergeGuards) {
			r := d.Reviews.Nodes[1]
			r.State, r.SubmittedAt = "DISMISSED", time.Unix(4, 0)
			d.Reviews.Nodes = append(d.Reviews.Nodes, r)
		}, "DISMISSED"},
		{"method not allowed", func(d *prDetail, g *mergeGuards) { d.Repository.SquashMergeAllowed = false }, "squash isn't allowed"},
		{"draft", func(d *prDetail, g *mergeGuards) { d.IsDraft = true }, "draft"},
		{"merged", func(d *prDetail, g *mergeGuards) { d.State = "MERGED" }, "merged"},
		{"required check pending", func(d *prDetail, g *mergeGuards) {
			json.Unmarshal([]byte(`{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
				{"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","isRequired":true},
				{"__typename":"CheckRun","name":"optional","conclusion":"FAILURE"}]}}}}]}`), &d.Commits)
		}, "required checks not passing: test (in_progress)"},
	}
	for _, c := range cases {
		d, g := guardedPR(), ok
		c.edit(d, &g)
		if why := mergeRefusal(d, squash, g); !strings.Contains(why, c.want) {
			t.Errorf("%s: want %q, got %q", c.name, c.want, why)
		}
	}
}

func TestCLIMergeRefusesWithoutMerging(t *testing.T) {
	ghToken = "test"
	t.Cleanup(func() { ghToken = "" })
	reqs := fakeGitHub(t, map[string]string{"POST /graphql": "200 " + `{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"abc","changedFiles":2,"repository":{"squashMergeAllowed":true}}}}}`})
	if code := cliMergeCommand([]string{"superultrainc/sup#7", "--squash", "--max-files", "0"}); code != exitRefused {
		t.Fatalf("want exit %d, got %d", exitRefused, code)
	}
	if len(*reqs) != 1 {
		t.Fatalf("a refused merge should only read the PR, sent %d requests", len(*reqs))
	}
	if code := cliMergeCommand([]string{"superultrainc/sup#7"}); code != exitUsage {
		t.Fatalf("no method should be a usage error, got %d", code)
	}
}
