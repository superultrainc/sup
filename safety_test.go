package main

import (
	"encoding/json"
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
