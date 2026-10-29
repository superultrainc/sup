package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type openPRMsg struct{ pr PR }

// parsePRArg accepts a PR URL, owner/repo#N, or N / #N (repo from the current dir's origin).
func parsePRArg(arg string) (PR, error) {
	var pr PR
	s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(arg, "https://"), "http://"), "/")
	var slug, num string
	if rest, ok := strings.CutPrefix(s, "github.com/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return pr, fmt.Errorf("not a PR URL: %s", arg)
		}
		slug, num = parts[0]+"/"+parts[1], parts[3]
	} else if i := strings.LastIndex(s, "#"); i > 0 {
		slug, num = s[:i], s[i+1:]
	} else {
		num = strings.TrimPrefix(s, "#")
		var err error
		if slug, err = originSlug(); err != nil {
			return pr, err
		}
	}
	n, err := strconv.Atoi(num)
	owner, name, ok := strings.Cut(slug, "/")
	if err != nil || n <= 0 || !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return pr, fmt.Errorf("can't parse PR %q (use a URL, owner/repo#N, or N inside a repo)", arg)
	}
	pr.Number = n
	pr.Repository.Owner.Login, pr.Repository.Name = owner, name
	return pr, nil
}

// originSlug returns owner/repo for the current dir's origin remote.
func originSlug() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repo with an origin remote")
	}
	return slugFromRemote(strings.TrimSpace(string(out)))
}

func slugFromRemote(url string) (string, error) {
	_, path, ok := strings.Cut(url, "github.com")
	if !ok {
		return "", fmt.Errorf("origin is not a GitHub remote: %s", url)
	}
	return strings.TrimSuffix(strings.Trim(path, ":/"), ".git"), nil
}

// printCompletions lists cached PRs as zsh _describe entries; no network so tab stays instant.
func printCompletions() {
	for _, pr := range loadCachedPRs() {
		title := strings.ReplaceAll(pr.Title, ":", `\:`)
		fmt.Printf("%s/%s#%d:%s\n", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number, title)
	}
}

const zshCompletion = `_sup() {
  local -a prs
  prs=("${(@f)$(command sup --complete 2>/dev/null)}")
  _arguments '(-m --mine)'{-m,--mine}'[show PRs involving you]' '1:PR:{_describe PR prs}'
}
compdef _sup sup
`
