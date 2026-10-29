package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type openPRMsg struct{ pr PR }

// parsePRArgs accepts a PR URL, owner/repo#N, repo#N, `repo N`, or N / #N (repo from the current dir's origin).
func parsePRArgs(args []string, prs []PR, orgs []string) (PR, error) {
	var pr PR
	var slug, num string
	switch len(args) {
	case 2:
		slug, num = args[0], strings.TrimPrefix(args[1], "#")
	case 1:
		s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(args[0], "https://"), "http://"), "/")
		if rest, ok := strings.CutPrefix(s, "github.com/"); ok {
			parts := strings.Split(rest, "/")
			if len(parts) < 4 || parts[2] != "pull" {
				return pr, fmt.Errorf("not a PR URL: %s", args[0])
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
	default:
		return pr, fmt.Errorf("expected one PR (or a repo and a number), got %d args", len(args))
	}
	if !strings.Contains(slug, "/") {
		var err error
		if slug, err = resolveRepo(slug, prs, orgs); err != nil {
			return pr, err
		}
	}
	n, err := strconv.Atoi(num)
	owner, name, ok := strings.Cut(slug, "/")
	if err != nil || n <= 0 || !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return pr, fmt.Errorf("can't parse PR %q (use a URL, owner/repo#N, repo N, or N inside a repo)", strings.Join(args, " "))
	}
	pr.Number = n
	pr.Repository.Owner.Login, pr.Repository.Name = owner, name
	return pr, nil
}

// resolveRepo maps a repo name or unique substring to owner/repo using listed PRs, else the only org.
func resolveRepo(q string, prs []PR, orgs []string) (string, error) {
	var exact, partial []string
	seen := map[string]bool{}
	for _, pr := range prs {
		slug := pr.Repository.Owner.Login + "/" + pr.Repository.Name
		if seen[slug] {
			continue
		}
		seen[slug] = true
		switch name := strings.ToLower(pr.Repository.Name); {
		case name == strings.ToLower(q):
			exact = append(exact, slug)
		case strings.Contains(name, strings.ToLower(q)):
			partial = append(partial, slug)
		}
	}
	for _, m := range [][]string{exact, partial} {
		if len(m) == 1 {
			return m[0], nil
		}
		if len(m) > 1 {
			sort.Strings(m)
			return "", fmt.Errorf("%q matches %s", q, strings.Join(m, ", "))
		}
	}
	if len(orgs) == 1 {
		return orgs[0] + "/" + q, nil
	}
	return "", fmt.Errorf("unknown repo %q", q)
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

// completions lists PRs as zsh _describe entries: N for a given repo, else repo#N (owner/ only when the name is ambiguous).
func completions(repo string, prs []PR, orgs []string) []string {
	var out []string
	add := func(id, title string) { out = append(out, id+":"+strings.ReplaceAll(title, ":", `\:`)) }
	if repo != "" {
		slug, err := resolveRepo(repo, prs, orgs)
		if err != nil {
			return nil
		}
		for _, pr := range prs {
			if pr.Repository.Owner.Login+"/"+pr.Repository.Name == slug {
				add(strconv.Itoa(pr.Number), pr.Title)
			}
		}
		return out
	}
	owners := map[string]map[string]bool{} // repo name -> owners
	for _, pr := range prs {
		if owners[pr.Repository.Name] == nil {
			owners[pr.Repository.Name] = map[string]bool{}
		}
		owners[pr.Repository.Name][pr.Repository.Owner.Login] = true
	}
	for _, pr := range prs {
		id := fmt.Sprintf("%s#%d", pr.Repository.Name, pr.Number)
		if len(owners[pr.Repository.Name]) > 1 {
			id = pr.Repository.Owner.Login + "/" + id
		}
		add(id, pr.Title)
	}
	return out
}

// Reads the local cache only, so tab stays instant.
const zshCompletion = `_sup() {
  local -a prs
  _arguments '(-m --mine)'{-m,--mine}'[show PRs involving you]' \
    '1:PR:{prs=(${(f)"$(command sup --complete 2>/dev/null)"}); _describe PR prs}' \
    '2:PR:{prs=(${(f)"$(command sup --complete $line[1] 2>/dev/null)"}); _describe PR prs}'
}
compdef _sup sup
`
