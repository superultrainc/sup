package main

import (
	"strings"
	"testing"
)

func TestResolveOnlyAuthorsUnresolvedThreads(t *testing.T) {
	ghToken = "test"
	t.Cleanup(func() { ghToken = "" })
	reqs := fakeGitHub(t, map[string]string{"POST /graphql": "200 " + `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
		{"id":"T1","path":"a.go","line":1,"isResolved":false,"comments":{"nodes":[{"author":{"login":"greptile-apps"}},{"author":{"login":"neil"}}]}},
		{"id":"T2","path":"b.go","line":2,"isResolved":true,"comments":{"nodes":[{"author":{"login":"greptile-apps"}}]}},
		{"id":"T3","path":"c.go","line":3,"isResolved":false,"comments":{"nodes":[{"author":{"login":"neil"}},{"author":{"login":"greptile-apps"}}]}}
	]}}}}}`})
	if code := cliResolveCommand([]string{"superultrainc/sup#7", "--author", "greptile-apps[bot]"}); code != exitOK {
		t.Fatalf("got exit %d", code)
	}
	var resolved []string
	for _, r := range (*reqs)[1:] {
		q, _ := r.body["query"].(string)
		if strings.Contains(q, "resolveReviewThread") {
			resolved = append(resolved, q)
		}
	}
	if len(resolved) != 1 || !strings.Contains(resolved[0], `"T1"`) {
		t.Fatalf("should resolve only T1, sent %v", resolved)
	}

	*reqs = nil
	cliResolveCommand([]string{"superultrainc/sup#7", "--all"})
	if len(*reqs) != 3 { // detail, T1, T3
		t.Fatalf("--all should resolve both unresolved threads, sent %d requests", len(*reqs))
	}
}

func TestResolveNeedsAuthorOrAll(t *testing.T) {
	for _, args := range [][]string{{"superultrainc/sup#7"}, {"superultrainc/sup#7", "--all", "--author", "x"}} {
		if code := cliResolveCommand(args); code != exitUsage {
			t.Errorf("%v: want usage error, got %d", args, code)
		}
	}
}
