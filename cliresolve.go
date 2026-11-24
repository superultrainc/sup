package main

import (
	"flag"
	"fmt"
)

func init() { cliCommands["resolve"] = cliResolveCommand }

// threadsToResolve are the unresolved threads, only those author started when it's set.
func threadsToResolve(d *prDetail, author string) []reviewThread {
	var out []reviewThread
	for _, t := range d.ReviewThreads.Nodes {
		if t.IsResolved || (author != "" && (len(t.Comments.Nodes) == 0 || !sameLogin(t.Comments.Nodes[0].Author.Login, author))) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func cliResolveCommand(args []string) int {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	author := fs.String("author", "", "")
	all := fs.Bool("all", false, "")
	ref, err := parseCLIFlags(fs, args)
	// Resolving everything takes --all, so a forgotten --author can't close a human's threads.
	if err != nil || len(ref) == 0 || (*author == "") == !*all {
		return cliFail(exitUsage, "usage: sup resolve <ref> --author <login> | --all")
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
	resolved := 0
	for _, t := range threadsToResolve(d, *author) {
		out, err := graphqlPOST(fmt.Sprintf(`mutation { resolveReviewThread(input: {threadId: %q}) { thread { isResolved } } }`, t.ID))
		if err == nil {
			err = graphqlErrors(out)
		}
		if err != nil {
			return cliFail(exitError, "resolved %d threads on %s, then %s failed: %v", resolved, slug, threadLoc(t), err)
		}
		resolved++
		fmt.Printf("Resolved %s\n", threadLoc(t))
	}
	fmt.Printf("Resolved %s on %s\n", plural(resolved, "thread"), slug)
	return exitOK
}
