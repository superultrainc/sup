package main

import (
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type openPRMsg struct{ pr PR }

// parsePRArgs accepts a PR URL, owner/repo#N, repo#N, `repo [words...] N`, or N / #N (repo from the current dir's origin).
func parsePRArgs(args []string, prs []PR, orgs []string) (PR, error) {
	var pr PR
	var slug, num string
	switch len(args) {
	case 0:
		return pr, fmt.Errorf("expected a PR")
	default:
		slug, num = strings.Join(args[:len(args)-1], " "), strings.TrimPrefix(args[len(args)-1], "#")
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
	}
	n, err := strconv.Atoi(num)
	if !strings.Contains(slug, "/") {
		var rerr error
		if slug, rerr = resolveRepo(slug, n, prs, orgs); rerr != nil {
			return pr, rerr
		}
	}
	owner, name, ok := strings.Cut(slug, "/")
	if err != nil || n <= 0 || !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return pr, fmt.Errorf("can't parse PR %q (use a URL, owner/repo#N, repo N, or N inside a repo)", strings.Join(args, " "))
	}
	pr.Number = n
	pr.Repository.Owner.Login, pr.Repository.Name = owner, name
	return pr, nil
}

// resolveRepo maps a repo query to owner/repo using listed PRs, else the only org.
// Ignoring case and punctuation, it tries exact, then substring, then in-order letters ("supermac");
// ties go to the one repo with listed PR n.
func resolveRepo(q string, n int, prs []PR, orgs []string) (string, error) {
	nq := normRepo(q)
	var tiers [3][]string
	seen := map[string]bool{}
	for _, pr := range prs {
		slug := pr.Repository.Owner.Login + "/" + pr.Repository.Name
		if seen[slug] {
			continue
		}
		seen[slug] = true
		switch name := normRepo(pr.Repository.Name); {
		case name == nq:
			tiers[0] = append(tiers[0], slug)
		case strings.Contains(name, nq):
			tiers[1] = append(tiers[1], slug)
		case isSubseq(nq, name):
			tiers[2] = append(tiers[2], slug)
		}
	}
	for _, m := range tiers {
		if len(m) > 1 && n > 0 {
			var has []string
			for _, slug := range m {
				if slices.ContainsFunc(prs, func(pr PR) bool {
					return pr.Number == n && pr.Repository.Owner.Login+"/"+pr.Repository.Name == slug
				}) {
					has = append(has, slug)
				}
			}
			if len(has) == 1 {
				m = has
			}
		}
		if len(m) == 1 {
			return m[0], nil
		}
		if len(m) > 1 {
			sort.Strings(m)
			return "", fmt.Errorf("%q matches %s", q, strings.Join(m, ", "))
		}
	}
	if len(orgs) == 1 && !strings.Contains(q, " ") {
		return orgs[0] + "/" + q, nil
	}
	return "", fmt.Errorf("unknown repo %q", q)
}

func normRepo(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func isSubseq(sub, s string) bool {
	for _, r := range sub {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+1:]
	}
	return true
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
	// Skip an SSH host alias suffix, as in git@github.com-work:org/repo.
	if i := strings.IndexAny(path, ":/"); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(strings.Trim(path, "/"), ".git"), nil
}

// completions lists PRs as zsh _describe entries: N for a given repo, else repo#N (owner/ only when the name is ambiguous).
func completions(repo string, prs []PR, orgs []string) []string {
	var out []string
	add := func(id, title string) { out = append(out, id+":"+strings.ReplaceAll(title, ":", `\:`)) }
	if repo != "" {
		slug, err := resolveRepo(repo, 0, prs, orgs)
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
