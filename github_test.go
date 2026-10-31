package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordedRequest struct {
	method, path string
	body         map[string]any
}

// fakeGitHub records requests and answers each path with the given status/body.
func fakeGitHub(t *testing.T, responses map[string]string) *[]recordedRequest {
	t.Helper()
	var reqs []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		rec := recordedRequest{method: r.Method, path: r.URL.EscapedPath()}
		json.Unmarshal(data, &rec.body)
		reqs = append(reqs, rec)
		if resp, ok := responses[r.Method+" "+r.URL.EscapedPath()]; ok {
			status, body, _ := strings.Cut(resp, " ")
			if status == "422" {
				w.WriteHeader(422)
			}
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	old := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = old })
	return &reqs
}

func TestSubmitReviewIsNative(t *testing.T) {
	reqs := fakeGitHub(t, nil)
	pr := testPR("sup", "neil", 7, false, "")
	msg := submitReviewCmd("request-changes", pr, "please fix")().(reviewSubmittedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	r := (*reqs)[0]
	if r.method != "POST" || r.path != "/repos/superultrainc/sup/pulls/7/reviews" ||
		r.body["event"] != "REQUEST_CHANGES" || r.body["body"] != "please fix" {
		t.Fatalf("unexpected request: %+v", r)
	}
}

func TestSaveReviewersSplitsTeams(t *testing.T) {
	reqs := fakeGitHub(t, nil)
	pr := testPR("sup", "neil", 7, false, "")
	msg := saveReviewersCmd(pr, []string{"max", "superultrainc/backend"}, []string{"ben"})().(reviewersSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	add, remove := (*reqs)[0], (*reqs)[1]
	if add.method != "POST" || remove.method != "DELETE" || add.path != "/repos/superultrainc/sup/pulls/7/requested_reviewers" {
		t.Fatalf("unexpected requests: %+v / %+v", add, remove)
	}
	if got := add.body["team_reviewers"].([]any); len(got) != 1 || got[0] != "backend" {
		t.Errorf("team slug not split out: %v", add.body)
	}
	if got := add.body["reviewers"].([]any); len(got) != 1 || got[0] != "max" {
		t.Errorf("user reviewers wrong: %v", add.body)
	}
}

func TestMergePinsHeadAndDeletesBranch(t *testing.T) {
	reqs := fakeGitHub(t, map[string]string{
		"DELETE /repos/superultrainc/sup/git/refs/heads/feature/x": "422 " + `{"message":"Reference does not exist"}`,
	})
	pr := testPR("sup", "neil", 7, false, "")
	d := &prDetail{HeadRefOid: "abc123", HeadRefName: "feature/x"}
	md := mergeDialog{deleteBranch: true}
	ref, err := mergePR(pr, d, md, allMergeMethods[0])
	if err != nil || ref != "feature/x" {
		t.Fatalf("merge should succeed and hand back the branch, got %q, %v", ref, err)
	}
	if msg := deleteBranchCmd(pr, ref)().(branchDeletedMsg); msg.err != nil {
		t.Fatalf("an already-deleted branch is fine, got %v", msg.err)
	}
	merge, del := (*reqs)[0], (*reqs)[1]
	if merge.method != "PUT" || merge.path != "/repos/superultrainc/sup/pulls/7/merge" ||
		merge.body["merge_method"] != "squash" || merge.body["sha"] != "abc123" {
		t.Fatalf("unexpected merge request: %+v", merge)
	}
	if del.method != "DELETE" || del.path != "/repos/superultrainc/sup/git/refs/heads/feature/x" {
		t.Fatalf("unexpected branch delete: %+v", del)
	}

	// A fork's branch is never deleted.
	*reqs = nil
	d.IsCrossRepo = true
	if ref, _ := mergePR(pr, d, md, allMergeMethods[0]); ref != "" || len(*reqs) != 1 {
		t.Fatalf("fork merge should not delete a branch, sent %d requests", len(*reqs))
	}
}

func TestAutoMergeUsesMutation(t *testing.T) {
	reqs := fakeGitHub(t, nil)
	pr := testPR("sup", "neil", 7, false, "")
	d := &prDetail{ID: "PR_kwDO123", HeadRefOid: "abc"}
	if _, err := mergePR(pr, d, mergeDialog{auto: true}, allMergeMethods[0]); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	q, _ := r.body["query"].(string)
	if r.path != "/graphql" || !strings.Contains(q, "enablePullRequestAutoMerge") || !strings.Contains(q, `"PR_kwDO123"`) || !strings.Contains(q, "SQUASH") {
		t.Fatalf("unexpected auto-merge request: %+v", r)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	fakeGitHub(t, map[string]string{"PUT /repos/superultrainc/sup/pulls/7/merge": "422 " + `{"message":"Head branch was modified","errors":[{"message":"Review and try again"}]}`})
	_, err := githubREST("PUT", "/repos/superultrainc/sup/pulls/7/merge", map[string]string{})
	if err == nil || err.Error() != "Head branch was modified: Review and try again" {
		t.Fatalf("got %v", err)
	}
}
