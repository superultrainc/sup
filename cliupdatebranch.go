package main

import (
	"encoding/json"
	"fmt"
)

func init() { cliCommands["update-branch"] = cliUpdateBranchCommand }

func cliUpdateBranchCommand(args []string) int {
	if len(args) == 0 {
		return cliFail(exitUsage, "usage: sup update-branch <ref>")
	}
	pr, err := cliPR(args)
	if err != nil {
		return cliFail(exitUsage, "%v", err)
	}
	slug := fmt.Sprintf("%s/%s#%d", pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
	out, err := githubREST("GET", fmt.Sprintf("%s/pulls/%d", repoPath(pr), pr.Number), nil)
	var r struct {
		Head struct {
			Sha string `json:"sha"`
		} `json:"head"`
	}
	if err == nil {
		err = json.Unmarshal(out, &r)
	}
	if err == nil {
		err = updateBranch(pr, r.Head.Sha)
	}
	if err != nil {
		return cliFail(exitError, "updating %s: %v", slug, err)
	}
	fmt.Printf("Updating %s from %.9s with its base branch\n", slug, r.Head.Sha)
	return exitOK
}
