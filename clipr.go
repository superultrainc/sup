package main

import (
	"flag"
	"time"
)

func init() { cliCommands["pr"] = cliPRCommand }

type prJSONCheck struct {
	Name       string `json:"name"`
	Result     string `json:"result"` // SUCCESS, FAILURE, NEUTRAL, SKIPPED, PENDING, IN_PROGRESS, ...
	IsRequired bool   `json:"isRequired"`
}

type prJSONReview struct {
	Author      string    `json:"author"`
	State       string    `json:"state"`
	CommitOid   string    `json:"commitOid"`
	SubmittedAt time.Time `json:"submittedAt"`
	Body        string    `json:"body"`
}

type prJSONThread struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Line       *int   `json:"line"`
	Author     string `json:"author"` // who started the thread
	IsResolved bool   `json:"isResolved"`
	IsOutdated bool   `json:"isOutdated"`
	Comments   int    `json:"comments"`
}

// prJSON is the stable output of `sup pr --json`; fields are only ever added.
type prJSON struct {
	Repo             string         `json:"repo"`
	Number           int            `json:"number"`
	ID               string         `json:"id"`
	URL              string         `json:"url"`
	Title            string         `json:"title"`
	State            string         `json:"state"`
	IsDraft          bool           `json:"isDraft"`
	Author           string         `json:"author"`
	BaseRefName      string         `json:"baseRefName"`
	HeadRefName      string         `json:"headRefName"`
	HeadRefOid       string         `json:"headRefOid"`
	Additions        int            `json:"additions"`
	Deletions        int            `json:"deletions"`
	ChangedFiles     int            `json:"changedFiles"`
	Mergeable        string         `json:"mergeable"`
	MergeStateStatus string         `json:"mergeStateStatus"`
	ReviewDecision   string         `json:"reviewDecision"`
	ChecksState      string         `json:"checksState"`
	Checks           []prJSONCheck  `json:"checks"`
	Reviews          []prJSONReview `json:"reviews"`
	ReviewThreads    []prJSONThread `json:"reviewThreads"`
}

func newPRJSON(pr PR, d *prDetail) prJSON {
	out := prJSON{
		Repo: pr.Repository.Owner.Login + "/" + pr.Repository.Name, Number: pr.Number,
		ID: d.ID, URL: d.URL, Title: d.Title, State: d.State, IsDraft: d.IsDraft, Author: d.Author.Login,
		BaseRefName: d.BaseRefName, HeadRefName: d.HeadRefName, HeadRefOid: d.HeadRefOid,
		Additions: d.Additions, Deletions: d.Deletions, ChangedFiles: d.ChangedFiles,
		Mergeable: d.Mergeable, MergeStateStatus: d.MergeStateStatus, ReviewDecision: d.ReviewDecision,
		Checks: []prJSONCheck{}, Reviews: []prJSONReview{}, ReviewThreads: []prJSONThread{},
	}
	for _, c := range d.Commits.Nodes {
		if r := c.Commit.StatusCheckRollup; r != nil {
			out.ChecksState = r.State
			for _, ctx := range r.Contexts.Nodes {
				out.Checks = append(out.Checks, prJSONCheck{ctx.displayName(), ctx.result(), ctx.IsRequired})
			}
		}
	}
	for _, r := range d.Reviews.Nodes {
		out.Reviews = append(out.Reviews, prJSONReview{r.Author.Login, r.State, r.Commit.Oid, r.SubmittedAt, cleanBody(r.Body)})
	}
	for _, t := range d.ReviewThreads.Nodes {
		jt := prJSONThread{ID: t.ID, Path: t.Path, Line: t.Line, IsResolved: t.IsResolved, IsOutdated: t.IsOutdated, Comments: len(t.Comments.Nodes)}
		if len(t.Comments.Nodes) > 0 {
			jt.Author = t.Comments.Nodes[0].Author.Login
		}
		out.ReviewThreads = append(out.ReviewThreads, jt)
	}
	return out
}

func cliPRCommand(args []string) int {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	ref, err := parseCLIFlags(fs, args)
	if err != nil || len(ref) == 0 || !*asJSON {
		return cliFail(exitUsage, "usage: sup pr <ref> --json")
	}
	pr, err := cliPR(ref)
	if err != nil {
		return cliFail(exitUsage, "%v", err)
	}
	d, err := fetchPRDetail(pr)
	if err != nil {
		return cliFail(exitError, "%v", err)
	}
	if err := printJSON(newPRJSON(pr, d)); err != nil {
		return cliFail(exitError, "%v", err)
	}
	return exitOK
}
