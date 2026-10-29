package main

import "testing"

func TestParsePRArg(t *testing.T) {
	for _, arg := range []string{
		"https://github.com/acme/web/pull/42",
		"https://github.com/acme/web/pull/42/files",
		"github.com/acme/web/pull/42/",
		"acme/web#42",
	} {
		pr, err := parsePRArg(arg)
		if err != nil || pr.Number != 42 || pr.Repository.Owner.Login != "acme" || pr.Repository.Name != "web" {
			t.Errorf("%s: got %+v, %v", arg, pr, err)
		}
	}
	for _, arg := range []string{"acme/web#x", "web#42", "https://github.com/acme/web/issues/42", "a/b/c#1"} {
		if _, err := parsePRArg(arg); err == nil {
			t.Errorf("%s: expected error", arg)
		}
	}
}

func TestSlugFromRemote(t *testing.T) {
	for _, url := range []string{"git@github.com:acme/web.git", "https://github.com/acme/web.git", "https://github.com/acme/web"} {
		if got, err := slugFromRemote(url); err != nil || got != "acme/web" {
			t.Errorf("%s: got %q, %v", url, got, err)
		}
	}
}
