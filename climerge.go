package main

import (
	"flag"
	"fmt"
	"slices"
	"strings"
)

func init() { cliCommands["merge"] = cliMergeCommand }

type mergeGuards struct {
	requireApproval string
	maxLines        int
	maxFiles        int
	matchHead       string
}

// mergeRefusal reports why a headless merge must not go ahead, or "" when it can.
func mergeRefusal(d *prDetail, method mergeMethod, g mergeGuards) string {
	if d.State != "OPEN" {
		return "PR is " + strings.ToLower(d.State)
	}
	if d.IsDraft {
		return "PR is a draft"
	}
	if g.matchHead != "" && (len(g.matchHead) < 7 || !strings.HasPrefix(d.HeadRefOid, g.matchHead)) {
		return fmt.Sprintf("head is %s, not %s", d.HeadRefOid, g.matchHead)
	}
	if !slices.ContainsFunc(allowedMergeMethods(d), func(m mergeMethod) bool { return m.id == method.id }) {
		return fmt.Sprintf("%s isn't allowed on this repo or branch", strings.TrimPrefix(method.flag, "--"))
	}
	if lines := d.Additions + d.Deletions; g.maxLines >= 0 && lines > g.maxLines {
		return fmt.Sprintf("%d changed lines is over --max-lines %d", lines, g.maxLines)
	}
	if g.maxFiles >= 0 && d.ChangedFiles > g.maxFiles {
		return fmt.Sprintf("%d changed files is over --max-files %d", d.ChangedFiles, g.maxFiles)
	}
	if g.requireApproval != "" {
		if r := latestVerdict(d, g.requireApproval); r == nil {
			return fmt.Sprintf("%s hasn't approved", g.requireApproval)
		} else if r.State != "APPROVED" {
			return fmt.Sprintf("%s's latest review is %s, not APPROVED", g.requireApproval, r.State)
		} else if r.Commit.Oid != d.HeadRefOid {
			return fmt.Sprintf("%s approved %.9s, but the head is now %.9s", g.requireApproval, r.Commit.Oid, d.HeadRefOid)
		}
	}
	var failing []string
	for _, c := range d.Commits.Nodes {
		if r := c.Commit.StatusCheckRollup; r != nil {
			for _, ctx := range r.Contexts.Nodes {
				if res := ctx.result(); ctx.IsRequired && res != "SUCCESS" && res != "NEUTRAL" && res != "SKIPPED" {
					failing = append(failing, ctx.displayName()+" ("+strings.ToLower(res)+")")
				}
			}
		}
	}
	if len(failing) > 0 {
		return "required checks not passing: " + strings.Join(failing, ", ")
	}
	return ""
}

// latestVerdict is login's latest approval, change request or dismissal; comments don't change a verdict.
// shortcut: only sees the first 100 reviews the detail query fetches, fine until a PR has more.
func latestVerdict(d *prDetail, login string) *prReview {
	var latest *prReview
	for i, r := range d.Reviews.Nodes {
		if sameLogin(r.Author.Login, login) && r.State != "COMMENTED" && r.State != "PENDING" &&
			(latest == nil || !r.SubmittedAt.Before(latest.SubmittedAt)) {
			latest = &d.Reviews.Nodes[i]
		}
	}
	return latest
}

func cliMergeCommand(args []string) int {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	methods := map[string]*bool{}
	for _, m := range allMergeMethods {
		methods[m.id] = fs.Bool(strings.TrimPrefix(m.flag, "--"), false, "")
	}
	var g mergeGuards
	fs.StringVar(&g.requireApproval, "require-approval", "", "")
	fs.IntVar(&g.maxLines, "max-lines", -1, "")
	fs.IntVar(&g.maxFiles, "max-files", -1, "")
	fs.StringVar(&g.matchHead, "match-head", "", "")
	ref, err := parseCLIFlags(fs, args)
	var method mergeMethod
	picked := 0
	for _, m := range allMergeMethods {
		if *methods[m.id] {
			method, picked = m, picked+1
		}
	}
	if err != nil || len(ref) == 0 || picked != 1 {
		return cliFail(exitUsage, "usage: sup merge <ref> --squash|--merge|--rebase [--require-approval <login>] [--max-lines N] [--max-files N] [--match-head <sha>]")
	}
	pr, err := cliPR(ref)
	if err != nil {
		return cliFail(exitUsage, "%v", err)
	}
	d, err := fetchPRDetail(pr)
	if err != nil {
		return cliFail(exitError, "%v", err)
	}
	slug := fmt.Sprintf("%s/%s#%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
	if why := mergeRefusal(d, method, g); why != "" {
		return cliFail(exitRefused, "not merging %s: %s", slug, why)
	}
	// mergePR pins the merge to d.HeadRefOid, the head the guards just checked.
	deleteRef, queued, err := mergePR(pr, d, mergeDialog{pr: pr, deleteBranch: true}, method)
	if err != nil {
		return cliFail(exitError, "merging %s: %v", slug, err)
	}
	if queued {
		fmt.Printf("Queued %s at %.9s\n", slug, d.HeadRefOid)
		return exitOK
	}
	fmt.Printf("Merged %s (%s) at %.9s\n", slug, strings.TrimPrefix(method.flag, "--"), d.HeadRefOid)
	if deleteRef != "" {
		if msg := deleteBranchCmd(pr, deleteRef)().(branchDeletedMsg); msg.err != nil {
			cliFail(exitOK, "%v", msg.err)
		}
	}
	return exitOK
}
