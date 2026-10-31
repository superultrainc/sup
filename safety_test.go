package main

import (
	"strings"
	"testing"
)

func TestSearchErrorReplyIsAnError(t *testing.T) {
	fakeGitHub(t, map[string]string{"POST /graphql": `200 {"data":null,"errors":[{"message":"API rate limit exceeded"}]}`})
	if _, err := fetchSearchPage("is:pr", ""); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("an error reply must not read as an empty search, got %v", err)
	}
}
