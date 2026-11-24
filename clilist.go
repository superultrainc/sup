package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

func init() { cliCommands["list"] = cliListCommand }

// listJSON is one row of `sup list --json`; fields are only ever added.
type listJSON struct {
	Repo               string    `json:"repo"`
	Number             int       `json:"number"`
	URL                string    `json:"url"`
	Title              string    `json:"title"`
	Author             string    `json:"author"`
	HeadRefName        string    `json:"headRefName"`
	HeadRefOid         string    `json:"headRefOid"`
	IsDraft            bool      `json:"isDraft"`
	Additions          int       `json:"additions"`
	Deletions          int       `json:"deletions"`
	ReviewDecision     string    `json:"reviewDecision"`
	RequestedReviewers []string  `json:"requestedReviewers"`
	IsInMergeQueue     bool      `json:"isInMergeQueue"`
	AutoMerge          bool      `json:"autoMerge"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func newListJSON(pr PR) listJSON {
	repo := pr.Repository.Owner.Login + "/" + pr.Repository.Name
	row := listJSON{
		Repo: repo, Number: pr.Number, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, pr.Number),
		Title: pr.Title, Author: pr.Author.Login, HeadRefName: pr.HeadRefName, HeadRefOid: pr.HeadRefOid,
		IsDraft: pr.IsDraft, Additions: pr.Additions, Deletions: pr.Deletions, ReviewDecision: pr.ReviewDecision,
		RequestedReviewers: []string{}, IsInMergeQueue: pr.IsInMergeQueue, AutoMerge: pr.AutoMerge != nil, UpdatedAt: pr.UpdatedAt,
	}
	for _, n := range pr.ReviewRequests.Nodes {
		name := n.RequestedReviewer.Login
		if name == "" {
			name = n.RequestedReviewer.Name
		}
		row.RequestedReviewers = append(row.RequestedReviewers, name)
	}
	return row
}

// listOpenPRs runs the TUI's refresh headlessly: discover every shard, then fetch full rows in batches.
func listOpenPRs(shards []string) ([]PR, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	seen := map[string]bool{}
	var stubs []PR
	for _, q := range shards {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			msg := discoverCmd(q, 0)().(discoveredMsg)
			mu.Lock()
			defer mu.Unlock()
			if msg.err != nil && firstErr == nil {
				firstErr = msg.err
			}
			for _, s := range msg.stubs {
				if pr := s.asPR(); !seen[prKey(pr)] {
					seen[prKey(pr)] = true
					stubs = append(stubs, pr)
				}
			}
		}(q)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	var rows []PR
	for i := 0; i < len(stubs); i += prRefreshBatch {
		wg.Add(1)
		go func(batch []PR) {
			defer wg.Done()
			msg := fetchPRBatchCmd(batch, 0)().(prsRefreshedMsg)
			mu.Lock()
			defer mu.Unlock()
			if msg.err != nil && firstErr == nil {
				firstErr = msg.err
			}
			rows = append(rows, msg.prs...)
		}(stubs[i:min(i+prRefreshBatch, len(stubs))])
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sortPRs(rows)
	return rows, nil
}

func cliListCommand(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	mine := fs.Bool("mine", false, "")
	headPrefix := fs.String("head-prefix", "", "")
	repo := fs.String("repo", "", "")
	rest, err := parseCLIFlags(fs, args)
	if owner, name, ok := strings.Cut(*repo, "/"); *repo != "" && (!ok || owner == "" || name == "") {
		err = fmt.Errorf("--repo takes owner/repo")
	}
	if err != nil || len(rest) > 0 || !*asJSON {
		return cliFail(exitUsage, "usage: sup list --json [--mine] [--head-prefix X] [--repo owner/repo]")
	}
	if err := loadGHToken(); err != nil {
		return cliFail(exitError, "failed to read gh auth token. Run: gh auth login")
	}
	mineMode = *mine
	var shards []string
	switch {
	case *repo != "" && *mine:
		shards = []string{"repo:" + *repo + " involves:@me is:pr is:open"}
	case *repo != "":
		shards = []string{"repo:" + *repo + " is:pr is:open"}
	default:
		if orgs, err = listOrgs(); err != nil {
			return cliFail(exitError, "%v", err)
		}
		shards = searchShards()
	}
	prs, err := listOpenPRs(shards)
	if err != nil {
		return cliFail(exitError, "%v", err)
	}
	rows := []listJSON{}
	for _, pr := range prs {
		if strings.HasPrefix(pr.HeadRefName, *headPrefix) {
			rows = append(rows, newListJSON(pr))
		}
	}
	if err := printJSON(rows); err != nil {
		return cliFail(exitError, "%v", err)
	}
	return exitOK
}

// listOrgs picks orgs the way the TUI does: SUP_ORG, then the cached list, then GitHub.
func listOrgs() ([]string, error) {
	if env := os.Getenv("SUP_ORG"); env != "" {
		return strings.Split(env, ","), nil
	}
	if meta, ok := loadCachedMeta(); ok && len(meta.Orgs) > 0 {
		return meta.Orgs, nil
	}
	return fetchUserOrgs()
}
