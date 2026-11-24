package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListOpenPRsDiscoversThenFetchesRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body struct{ Query string }
		json.Unmarshal(data, &body)
		if strings.Contains(body.Query, "search(") {
			w.Write([]byte(`{"data":{"search":{"issueCount":2,"nodes":[
				{"number":1,"repository":{"name":"web","owner":{"login":"acme"}}},
				{"number":2,"repository":{"name":"web","owner":{"login":"acme"}}}]}}}`))
			return
		}
		w.Write([]byte(`{"data":{
			"p0":{"pullRequest":{"number":1,"headRefName":"agent/one","additions":3,"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"max"}},{"requestedReviewer":{"name":"backend"}}]}}},
			"p1":{"pullRequest":{"number":2,"headRefName":"fix/two","isDraft":true,"autoMergeRequest":{}}}}}`))
	}))
	t.Cleanup(srv.Close)
	old := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = old })

	prs, err := listOpenPRs([]string{"repo:acme/web is:pr is:open"})
	if err != nil || len(prs) != 2 {
		t.Fatalf("got %d PRs, %v", len(prs), err)
	}
	row := newListJSON(prs[0])
	if row.Repo != "acme/web" || row.Number != 1 || row.URL != "https://github.com/acme/web/pull/1" || row.Additions != 3 ||
		strings.Join(row.RequestedReviewers, ",") != "max,backend" {
		t.Fatalf("unexpected row: %+v", row)
	}
	if draft := newListJSON(prs[1]); !draft.IsDraft || !draft.AutoMerge {
		t.Fatalf("drafts sort last and keep auto-merge: %+v", draft)
	}
}
