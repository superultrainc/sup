package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const prDetailReply = `{"data":{"repository":{"pullRequest":{
	"id":"PR_1","state":"OPEN","headRefOid":"abc","additions":10,"deletions":2,"changedFiles":3,"mergeStateStatus":"CLEAN",
	"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS","contexts":{"nodes":[
		{"__typename":"CheckRun","name":"test","conclusion":"SUCCESS","isRequired":true,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
		{"__typename":"StatusContext","context":"lint","state":"PENDING","isRequired":false}]}}}}]},
	"reviews":{"nodes":[{"author":{"login":"greptile-apps"},"state":"APPROVED","commit":{"oid":"abc"},"submittedAt":"2026-11-24T17:18:11Z"}]},
	"reviewThreads":{"nodes":[{"id":"PRRT_1","path":"a.go","line":4,"isResolved":false,"comments":{"nodes":[{"author":{"login":"greptile-apps"}},{"author":{"login":"neil"}}]}}]}
}}}}`

func TestPRJSON(t *testing.T) {
	reqs := fakeGitHub(t, map[string]string{"POST /graphql": "200 " + prDetailReply})
	pr := testPR("sup", "neil", 7, false, "")
	d, err := fetchPRDetail(pr)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := (*reqs)[0].body["query"].(string)
	for _, want := range []string{"commit { oid }", "id path line", "isRequired(pullRequestNumber: 7)"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q", want)
		}
	}
	data, _ := json.Marshal(newPRJSON(pr, d))
	var got map[string]any
	json.Unmarshal(data, &got)
	if got["repo"] != "superultrainc/sup" || got["headRefOid"] != "abc" || got["checksState"] != "SUCCESS" || got["changedFiles"] != 3.0 {
		t.Fatalf("unexpected top level: %s", data)
	}
	check := got["checks"].([]any)[0].(map[string]any)
	if check["name"] != "CI / test" || check["result"] != "SUCCESS" || check["isRequired"] != true {
		t.Errorf("unexpected check: %v", check)
	}
	review := got["reviews"].([]any)[0].(map[string]any)
	if review["author"] != "greptile-apps" || review["commitOid"] != "abc" {
		t.Errorf("unexpected review: %v", review)
	}
	thread := got["reviewThreads"].([]any)[0].(map[string]any)
	if thread["id"] != "PRRT_1" || thread["author"] != "greptile-apps" || thread["isResolved"] != false || thread["comments"] != 2.0 {
		t.Errorf("unexpected thread: %v", thread)
	}
}
