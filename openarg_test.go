package main

import (
	"slices"
	"testing"
)

func repoPR(owner, repo string, n int, title string) PR {
	var pr PR
	pr.Repository.Owner.Login, pr.Repository.Name, pr.Number, pr.Title = owner, repo, n, title
	return pr
}

func TestParsePRArgs(t *testing.T) {
	prs := []PR{repoPR("acme", "web", 1, ""), repoPR("acme", "web-api", 2, ""), repoPR("acme", "api-docs", 3, "")}
	for _, args := range [][]string{
		{"https://github.com/acme/web/pull/42"},
		{"https://github.com/acme/web/pull/42/files"},
		{"github.com/acme/web/pull/42/"},
		{"acme/web#42"},
		{"web#42"}, // exact name beats substring
		{"web", "42"},
		{"WEB", "#42"},
	} {
		pr, err := parsePRArgs(args, prs, nil)
		if err != nil || pr.Number != 42 || pr.Repository.Owner.Login != "acme" || pr.Repository.Name != "web" {
			t.Errorf("%v: got %+v, %v", args, pr, err)
		}
	}
	if pr, err := parsePRArgs([]string{"docs", "7"}, prs, nil); err != nil || pr.Repository.Name != "api-docs" {
		t.Errorf("unique substring: got %+v, %v", pr, err)
	}
	if pr, err := parsePRArgs([]string{"other", "7"}, prs, []string{"acme"}); err != nil || pr.Repository.Name != "other" || pr.Repository.Owner.Login != "acme" {
		t.Errorf("single-org fallback: got %+v, %v", pr, err)
	}
	for _, args := range [][]string{{"acme/web#x"}, {"api", "1"}, {"nope#42"}, {"https://github.com/acme/web/issues/42"}, {"a/b/c#1"}, {"a", "b", "c"}} {
		if _, err := parsePRArgs(args, prs, nil); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
}

func TestCompletions(t *testing.T) {
	one := []PR{repoPR("acme", "web", 1, "Fix: login"), repoPR("acme", "api", 2, "Add x")}
	if got, want := completions("", one, nil), []string{`web#1:Fix\: login`, "api#2:Add x"}; !slices.Equal(got, want) {
		t.Errorf("single org: got %v", got)
	}
	if got, want := completions("ap", one, nil), []string{"2:Add x"}; !slices.Equal(got, want) {
		t.Errorf("repo filter: got %v", got)
	}
	two := append(one, repoPR("other", "cli", 3, "Bump"), repoPR("other", "web", 4, "Dup"))
	if got, want := completions("", two, nil), []string{`acme/web#1:Fix\: login`, "api#2:Add x", "cli#3:Bump", "other/web#4:Dup"}; !slices.Equal(got, want) {
		t.Errorf("multi org: got %v", got)
	}
}

func TestSlugFromRemote(t *testing.T) {
	for _, url := range []string{"git@github.com:acme/web.git", "https://github.com/acme/web.git", "https://github.com/acme/web"} {
		if got, err := slugFromRemote(url); err != nil || got != "acme/web" {
			t.Errorf("%s: got %q, %v", url, got, err)
		}
	}
}
