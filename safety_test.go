package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchErrorReplyIsAnError(t *testing.T) {
	fakeGitHub(t, map[string]string{"POST /graphql": `200 {"data":null,"errors":[{"message":"API rate limit exceeded"}]}`})
	if _, err := fetchSearchPage("is:pr", ""); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("an error reply must not read as an empty search, got %v", err)
	}
}

func TestControlCharactersAreStripped(t *testing.T) {
	raw := []byte(`{"title":"fix\u001b]52;c;aGk=\u0007 it\u009b","path":"C:\\u001b","n":"a\nb\tc"}`)
	var got map[string]string
	if err := json.Unmarshal(stripJSONControls(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "fix]52;c;aGk= it" || got["path"] != `C:\u001b` || got["n"] != "a\nb\tc" {
		t.Fatalf("got %q", got)
	}
	files := parseUnifiedDiff("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-old\n+new\x1b]0;pwned\x07\n")
	if line := files[0].hunks[0].lines[1].text; strings.ContainsAny(line, "\x1b\x07") {
		t.Fatalf("diff line kept control characters: %q", line)
	}
}

func TestRejectedReviewKeepsItsText(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	pr := testPR("sup", "neil", 7, false, "")
	fakeGitHub(t, map[string]string{"POST /repos/superultrainc/sup/pulls/7/reviews": `422 {"message":"Can not request changes on your own pull request"}`})
	msg := submitReviewCmd("request-changes", pr, "please rename this")()
	next, _ := initialModel().Update(msg)
	m := next.(model)
	path := filepath.Join(cacheDir(), "last-review.md")
	if data, err := os.ReadFile(path); err != nil || string(data) != "please rename this" || !strings.Contains(m.actionStatus, path) {
		t.Fatalf("text should be saved and its path shown, got %q, status %q", data, m.actionStatus)
	}
}

func TestFindRepoPathChecksOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUP_DEV_DIR", filepath.Join(home, "other"))
	clone := func(dir, origin string) {
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %s", out)
		}
		exec.Command("git", "-C", dir, "remote", "add", "origin", origin).Run()
	}
	clone(filepath.Join(home, "other", "api"), "git@github.com:someone-else/api.git")
	want := filepath.Join(home, "Development", "api")
	clone(want, "git@github.com-work:acme/api.git")
	if got := findRepoPath("acme", "api"); got != want {
		t.Fatalf("should skip the other org's clone, got %q", got)
	}
	if got := findRepoPath("nobody", "api"); got != "" {
		t.Fatalf("no clone of nobody/api exists, got %q", got)
	}
}
