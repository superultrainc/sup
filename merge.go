package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// mergeMethod is one of GitHub's merge button options.
type mergeMethod struct {
	id    string // MERGE, SQUASH, REBASE (GitHub's enum)
	label string
	flag  string
}

var allMergeMethods = []mergeMethod{
	{"SQUASH", "Squash and merge", "--squash"},
	{"MERGE", "Create a merge commit", "--merge"},
	{"REBASE", "Rebase and merge", "--rebase"},
}

// mergeDialog holds only choices; what's allowed is read from the PR detail
// each render, so the dialog works while the detail is still loading.
type mergeDialog struct {
	method       string
	deleteBranch bool
	auto         bool
	admin        bool
	saving       bool
	err          error
}

func newMergeDialog(d *prDetail) *mergeDialog {
	md := &mergeDialog{deleteBranch: true}
	if d != nil {
		md.method = d.Repository.ViewerDefaultMergeMethod
		md.auto = d.MergeStateStatus == "BLOCKED" && !d.ViewerCanMergeAsAdmin
	}
	return md
}

func allowedMergeMethods(d *prDetail) []mergeMethod {
	if d == nil {
		return nil
	}
	var out []mergeMethod
	for _, m := range allMergeMethods {
		switch {
		case m.id == "SQUASH" && d.Repository.SquashMergeAllowed,
			m.id == "MERGE" && d.Repository.MergeCommitAllowed,
			m.id == "REBASE" && d.Repository.RebaseMergeAllowed:
			out = append(out, m)
		}
	}
	return out
}

// selectedMethod falls back to the first allowed method when the stored one isn't allowed.
func (md *mergeDialog) selectedMethod(d *prDetail) (mergeMethod, bool) {
	methods := allowedMergeMethods(d)
	want := md.method
	if want == "" && d != nil {
		want = d.Repository.ViewerDefaultMergeMethod
	}
	for _, m := range methods {
		if m.id == want {
			return m, true
		}
	}
	if len(methods) > 0 {
		return methods[0], true
	}
	return mergeMethod{}, false
}

// mergeState explains GitHub's merge-button state; ok is 1 ready, 0 caution, -1 can't merge now.
func mergeState(d *prDetail) (ok int, text string) {
	switch d.MergeStateStatus {
	case "CLEAN":
		return 1, "This branch has no conflicts and all requirements are met"
	case "HAS_HOOKS":
		return 1, "Ready to merge (pre-receive hooks will run)"
	case "UNSTABLE":
		return 0, "Some checks are failing or pending, but merging is allowed"
	case "BEHIND":
		return 0, "This branch is out of date with the base branch"
	case "BLOCKED":
		return -1, "Merging is blocked: required reviews or checks aren't satisfied"
	case "DIRTY":
		return -1, "This branch has conflicts that must be resolved"
	case "DRAFT":
		return -1, "This pull request is still a draft"
	}
	return 0, "Checking whether this branch can be merged…"
}

// blockReason reports why enter would be refused, or "" when merging can go ahead.
func (md *mergeDialog) blockReason(d *prDetail) string {
	if d == nil {
		return "Loading PR…"
	}
	if d.State != "OPEN" {
		return "This pull request is " + strings.ToLower(d.State)
	}
	if d.IsDraft {
		return "Draft: press W to mark it ready for review first"
	}
	if _, ok := md.selectedMethod(d); !ok {
		return "This repository doesn't allow any merge method you can use"
	}
	switch d.MergeStateStatus {
	case "DIRTY":
		return "Resolve conflicts before merging"
	case "BLOCKED":
		if !md.auto && !md.admin {
			return "Blocked: enable auto-merge (a) or bypass as admin (!)"
		}
	}
	return ""
}

type mergeDoneMsg struct {
	pr     PR
	method mergeMethod
	auto   bool
	ready  bool // marked ready for review rather than merged
	draft  bool // converted back to a draft
	closed bool // closed without merging
	err    error
}

func runCloseCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		msg := mergeDoneMsg{pr: pr, closed: true}
		if !demoMode {
			_, msg.err = githubREST("PATCH", fmt.Sprintf("%s/pulls/%d", repoPath(pr), pr.Number), map[string]string{"state": "closed"})
		}
		return msg
	}
}

// runDraftCmd converts pr to a draft, or marks it ready for review when draft is false.
func runDraftCmd(pr PR, draft bool) tea.Cmd {
	return func() tea.Msg {
		msg := mergeDoneMsg{pr: pr, draft: draft, ready: !draft}
		if demoMode {
			return msg
		}
		// List rows don't carry the node ID the mutation needs.
		out, err := graphqlPOST(fmt.Sprintf(`query { repository(owner: %q, name: %q) { pullRequest(number: %d) { id } } }`,
			pr.Repository.Owner.Login, pr.Repository.Name, pr.Number))
		var r struct {
			Data struct {
				Repository struct {
					PullRequest struct{ ID string } `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err == nil {
			err = graphqlErrors(out)
		}
		if err == nil {
			err = json.Unmarshal(out, &r)
		}
		if err == nil {
			mutation := "markPullRequestReadyForReview"
			if draft {
				mutation = "convertPullRequestToDraft"
			}
			out, err = graphqlPOST(fmt.Sprintf(`mutation { %s(input: {pullRequestId: %q}) { clientMutationId } }`, mutation, r.Data.Repository.PullRequest.ID))
		}
		if err == nil {
			err = graphqlErrors(out)
		}
		msg.err = err
		return msg
	}
}

func runMergeCmd(pr PR, d *prDetail, md mergeDialog, method mergeMethod) tea.Cmd {
	return func() tea.Msg {
		msg := mergeDoneMsg{pr: pr, method: method, auto: md.auto}
		if demoMode {
			return msg
		}
		msg.err = mergePR(pr, d, md, method)
		return msg
	}
}

// mergePR merges through the API. Merge queues need gh's queue handling, so
// that one case falls back to `gh pr merge`.
func mergePR(pr PR, d *prDetail, md mergeDialog, method mergeMethod) error {
	if md.auto {
		out, err := graphqlPOST(fmt.Sprintf(`mutation { enablePullRequestAutoMerge(input: {pullRequestId: %q, mergeMethod: %s}) { clientMutationId } }`, d.ID, method.id))
		if err != nil {
			return err
		}
		return graphqlErrors(out)
	}
	// sha makes GitHub refuse if commits landed after this view loaded, so nothing unreviewed merges.
	body := map[string]string{"merge_method": strings.ToLower(method.id), "sha": d.HeadRefOid}
	if _, err := githubREST("PUT", fmt.Sprintf("%s/pulls/%d/merge", repoPath(pr), pr.Number), body); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "merge queue") {
			return ghMerge(pr, d, md, method)
		}
		return err
	}
	// A fork's branch isn't ours to delete; 422 means the repo already deleted it.
	if md.deleteBranch && !d.IsCrossRepo && d.HeadRefName != "" {
		ref := url.PathEscape(d.HeadRefName)
		ref = strings.ReplaceAll(ref, "%2F", "/")
		if _, err := githubREST("DELETE", repoPath(pr)+"/git/refs/heads/"+ref, nil); err != nil && !strings.Contains(err.Error(), "Reference does not exist") {
			return fmt.Errorf("merged, but deleting the branch failed: %w", err)
		}
	}
	return nil
}

func ghMerge(pr PR, d *prDetail, md mergeDialog, method mergeMethod) error {
	args := []string{"pr", "merge", fmt.Sprint(pr.Number), "--repo", pr.Repository.Owner.Login + "/" + pr.Repository.Name, method.flag, "--match-head-commit", d.HeadRefOid}
	if md.deleteBranch {
		args = append(args, "--delete-branch")
	}
	if md.admin {
		args = append(args, "--admin")
	}
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return fmt.Errorf("%s", text)
		}
		return err
	}
	return nil
}

func (m model) handleMergeInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.detail
	md := v.merge
	d := v.detail
	if md.saving {
		return m, nil
	}
	methods := allowedMergeMethods(d)
	cur := 0
	if sel, ok := md.selectedMethod(d); ok {
		for i, mm := range methods {
			if mm.id == sel.id {
				cur = i
			}
		}
	}
	switch msg.String() {
	case "esc", "q":
		v.merge = nil
	case "up", "k":
		if len(methods) > 0 {
			md.method = methods[max(cur-1, 0)].id
		}
	case "down", "j":
		if len(methods) > 0 {
			md.method = methods[min(cur+1, len(methods)-1)].id
		}
	case "b":
		md.deleteBranch = !md.deleteBranch
	case "a":
		md.auto = !md.auto
		if md.auto {
			md.admin = false
		}
	case "!":
		if d != nil && d.ViewerCanMergeAsAdmin {
			md.admin = !md.admin
			if md.admin {
				md.auto = false
			}
		}
	case "enter":
		if md.blockReason(d) != "" {
			return m, nil
		}
		method, _ := md.selectedMethod(d)
		md.saving, md.err = true, nil
		return m, tea.Batch(runMergeCmd(v.pr, d, *md, method), spinnerTick())
	}
	return m, nil
}

func (m *model) handleMergeDone(msg mergeDoneMsg) tea.Cmd {
	v := m.detail
	if msg.err != nil {
		if v != nil && v.merge != nil {
			v.merge.saving, v.merge.err = false, msg.err
		} else {
			m.actionStatus = "Error: " + msg.err.Error()
		}
		return nil
	}
	if v != nil {
		v.merge = nil
	}
	var save tea.Cmd
	key := prKey(msg.pr)
	switch {
	case msg.ready, msg.draft:
		m.actionStatus = fmt.Sprintf("✓ Marked #%d ready for review", msg.pr.Number)
		if msg.draft {
			m.actionStatus = fmt.Sprintf("✓ Converted #%d to draft", msg.pr.Number)
		}
		for i := range m.prs {
			if prKey(m.prs[i]) == key {
				m.prs[i].IsDraft = msg.draft
			}
		}
		if v != nil && prKey(v.pr) == key {
			v.pr.IsDraft = msg.draft
		}
		sortPRs(m.prs) // drafts sort last
		m.applyFilterKeepingCursor()
		save = m.markPRsDirty()
	case msg.auto:
		m.actionStatus = fmt.Sprintf("✓ Auto-merge enabled for #%d (%s)", msg.pr.Number, strings.ToLower(msg.method.label))
	default:
		if msg.closed {
			m.actionStatus = fmt.Sprintf("✓ Closed #%d", msg.pr.Number)
		} else {
			m.actionStatus = fmt.Sprintf("✓ Merged #%d (%s)", msg.pr.Number, strings.ToLower(msg.method.label))
		}
		// The list only shows open PRs; drop it now rather than waiting for the next refresh.
		kept := m.prs[:0]
		for _, pr := range m.prs {
			if prKey(pr) != key {
				kept = append(kept, pr)
			}
		}
		m.prs = kept
		m.syncRepoList()
		m.applyFilterKeepingCursor()
		save = m.markPRsDirty()
	}
	if v != nil && prKey(v.pr) == key {
		e := m.cacheEntry(key)
		e.detailAt = e.detailAt.AddDate(-1, 0, 0) // force a refetch to show the new state
		v.loading = true
		return tea.Batch(append(m.fetchMissing(v.pr, e), spinnerTick(), save)...)
	}
	return save
}

func (m model) mergeView(height int) []string {
	v := m.detail
	md := v.merge
	d := v.detail
	th := currentDiffTheme()
	width := m.width - 2
	muted := sty(th.muted, "", false, false)
	text := sty(th.text, "", false, false)

	band := th.band
	left := sty(th.text, band, true, false).Render("Merge pull request")
	right := sty(th.muted, band, false, false).Render(fmt.Sprintf("#%d", v.pr.Number))
	var body []string

	switch {
	case d == nil:
		body = append(body, reviewRequestedStyle.Render(spinnerFrames[m.spinnerFrame]+" Loading PR..."))
	default:
		ok, state := mergeState(d)
		icon, color := "●", th.threadBorder
		switch ok {
		case 1:
			icon, color = "✓", th.addBar
		case -1:
			icon, color = "✗", th.delBar
		}
		body = append(body, sty(color, "", true, false).Render(icon)+"  "+text.Render(state))
		if d.AutoMergeRequest != nil {
			body = append(body, sty(th.addBar, "", false, false).Render("✓")+"  "+
				muted.Render("Auto-merge is already enabled ("+strings.ToLower(d.AutoMergeRequest.MergeMethod)+")"))
		}
		body = append(body, "")

		sel, _ := md.selectedMethod(d)
		for _, mm := range allowedMergeMethods(d) {
			caret, radio, label := "  ", muted.Render("○ "), text.Render(mm.label)
			if mm.id == sel.id {
				caret = caretStyle.Render("» ")
				radio = sty(th.addBar, "", true, false).Render("● ")
				label = sty(th.text, "", true, false).Render(mm.label)
			}
			body = append(body, caret+radio+label)
		}
		body = append(body, "")

		box := func(on bool) string {
			if on {
				return approvedStyle.Render("[✓] ")
			}
			return muted.Render("[ ] ")
		}
		body = append(body, box(md.deleteBranch)+text.Render("Delete ")+chip(d.HeadRefName, th.branchFg, th.branchBg)+text.Render(" after merge")+muted.Render("   b"))
		body = append(body, box(md.auto)+text.Render("Enable auto-merge")+muted.Render(" (merge once requirements are met)   a"))
		if d.ViewerCanMergeAsAdmin && d.MergeStateStatus == "BLOCKED" {
			body = append(body, box(md.admin)+text.Render("Bypass rules and merge as admin")+muted.Render("   !"))
		}
		body = append(body, "")

		switch {
		case md.saving:
			body = append(body, reviewRequestedStyle.Render(spinnerFrames[m.spinnerFrame]+" Merging..."))
		case md.err != nil:
			body = append(body, changesRequestedStyle.Render("Error: "+md.err.Error()))
		case md.blockReason(d) != "":
			body = append(body, changesRequestedStyle.Render(md.blockReason(d)))
		default:
			action := "merge"
			if md.auto {
				action = "enable auto-merge"
			}
			body = append(body, sty(th.addBar, "", true, false).Render("enter")+muted.Render(" "+action+" · esc cancel"))
		}
	}
	border := th.addBar
	if d != nil {
		if ok, _ := mergeState(d); ok < 0 {
			border = th.delBar
		} else if ok == 0 {
			border = th.threadBorder
		}
	}
	out := bandCard(left, right, body, width, border, band)
	for i := range out {
		out[i] = " " + out[i]
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}
