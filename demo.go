package main

import (
	"fmt"
	"sync"
	"time"
)

// demoNow anchors demo timestamps so repeated mockPRs calls agree.
var demoNow = time.Now()

type demoPR struct {
	repo    string
	number  int
	title   string
	author  string
	branch  string
	adds    int
	dels    int
	state   string // approved, denied, review, commented, draft
	summary string
}

// demoPRs fill out the demo list beyond the original eight mock rows.
var demoPRs = []demoPR{
	{"macos-app", 1048, "Add Today and This Month ranges to stats", "priya", "stats-time-ranges", 253, 38, "review", "Adds **Today** and **This Month** to the stats range picker, so recent usage is one click away."},
	{"macos-app", 1046, "Don't take focus when recording starts", "sarah", "fix/no-focus-steal", 3, 0, "approved", "Recording no longer pulls focus from the frontmost app, so typing continues uninterrupted."},
	{"macos-app", 1045, "Store license data in the keychain", "sarah", "license-keychain", 386, 648, "review", "Moves license data from `UserDefaults` to the keychain and migrates existing installs on first launch."},
	{"macos-app", 1044, "Install updates automatically on quit", "jordan", "auto-update-on-quit", 55, 6, "approved", "Downloads updates in the background and installs them when the app quits."},
	{"macos-app", 1042, "Move the update feed to the CDN", "jordan", "update-feed-cdn", 11, 6, "approved", "Serves the appcast from the CDN instead of the app server."},
	{"macos-app", 1041, "Reduce app and DMG size", "alex", "reduce-bundle-size", 22, 2, "commented", "Strips debug symbols from release builds and drops unused localizations: **212 MB → 141 MB**."},
	{"macos-app", 1038, "Resume a finished meeting recording", "taylor", "resume-meeting", 598, 36, "denied", "Lets you pick up a meeting recording after stopping it, appending to the same transcript."},
	{"macos-app", 1032, "Unload idle local models after 10 minutes", "chris", "unload-idle-models", 433, 74, "review", "Frees model memory when dictation has been idle for 10 minutes and reloads it on the next use."},
	{"macos-app", 1029, "Menu bar icon shows recording level", "sam", "menubar-level", 141, 12, "draft", "Animates the menu bar icon with the input level while recording."},
	{"macos-app", 1027, "Fix crash when the mic disconnects mid-recording", "priya", "fix/mic-disconnect", 47, 9, "approved", "Stops the recording cleanly and keeps the audio so far when the input device goes away."},
	{"ios-app", 261, "Keyboard: swipe left to delete a word", "jordan", "feat/swipe-delete", 211, 2, "review", "Swiping left on the keyboard deletes the previous word, matching the system keyboard."},
	{"ios-app", 258, "3.1 release notes and version bump", "mike", "release/3.1", 37, 13, "approved", "Bumps the version to 3.1 and adds release notes."},
	{"ios-app", 255, "Emoji search in the keyboard", "jordan", "feat/emoji-search", 341, 24, "commented", "Adds a search field to the emoji panel with keyword matching."},
	{"ios-app", 251, "Haptics on key press", "sam", "feat/key-haptics", 64, 3, "draft", "Plays a light haptic on each key press when enabled in settings."},
	{"ios-app", 249, "Fix dictation stopping after 60 seconds", "priya", "fix/dictation-timeout", 18, 4, "denied", "Restarts the audio session before the 60 second system limit so long dictations keep going."},
	{"web-app", 402, "Billing page shows the next invoice", "taylor", "billing-next-invoice", 188, 21, "review", "Shows the next invoice date and amount on the billing page."},
	{"web-app", 400, "Refund policy copy", "alex", "copy/refund-policy", 43, 42, "approved", "Updates the refund policy to the new 14 day window."},
	{"web-app", 398, "Add meeting recorder landing page", "sam", "landing/meetings", 2362, 104, "review", "New landing page for meeting recording with demo video and FAQ."},
	{"web-app", 395, "Team invites by email domain", "chris", "team-domain-invites", 512, 87, "denied", "Admins can let anyone with a company email join the team without an invite."},
	{"web-app", 391, "Dark mode for the dashboard", "sam", "dashboard-dark", 820, 310, "draft", "Adds dark mode to the account dashboard, following the system setting."},
	{"backend-api", 411, "Refund the latest charge on cancellation", "alex", "refund-on-cancel", 944, 125, "review", "Refunds the most recent charge automatically when a subscription is cancelled within the refund window."},
	{"backend-api", 410, "Disable org licenses when a member leaves", "mike", "org-member-leave", 129, 6, "approved", "Frees the seat and disables the license as soon as a member leaves an organization."},
	{"backend-api", 409, "Clear org trial expiry on upgrade", "mike", "org-trial-expiry", 184, 2, "review", "Upgrading during a trial now clears the expiry so the org doesn't lapse."},
	{"backend-api", 408, "Add more free-mail domains", "mike", "free-mail-domains", 130, 36, "approved", "Adds 40 more free-mail providers to the list used to block team signups."},
	{"backend-api", 404, "Expire org licenses at trial end", "taylor", "org-trial-pause", 438, 6, "commented", "A nightly job expires organization licenses whose trial has ended without a payment method."},
	{"backend-api", 399, "Rate limit transcription by plan", "chris", "plan-rate-limits", 276, 58, "draft", "Applies per-plan rate limits to the transcription endpoint."},
	{"job-runner", 293, "Retry failed jobs with backoff", "alex", "job-retry-backoff", 167, 40, "review", "Failed jobs retry up to five times with exponential backoff before landing in the dead letter queue."},
	{"job-runner", 290, "Graceful shutdown on SIGTERM", "jordan", "graceful-shutdown", 72, 15, "approved", "Workers finish their current job before exiting on SIGTERM."},
	{"data-service", 160, "Nightly usage export to the warehouse", "priya", "usage-export", 389, 12, "review", "Exports daily usage to the warehouse at 02:00 UTC."},
	{"data-service", 158, "Drop the legacy events table", "chris", "drop-legacy-events", 12, 1840, "denied", "Removes the events table replaced by the partitioned version in #142."},
	{"cli-tools", 207, "Add --json output to every command", "alex", "json-output", 318, 77, "review", "Every command accepts `--json` for scripting."},
	{"cli-tools", 205, "Shell completions for zsh and fish", "sam", "completions", 245, 0, "approved", "Generates completions with `cli completions zsh|fish`."},
	{"docs", 88, "Document the meeting recorder", "taylor", "docs/meetings", 410, 22, "review", "New guide covering meeting recording, speakers and exports."},
	{"docs", 86, "Weekly docs check", "mike", "docs/weekly-check", 3, 3, "approved", "Fixes three broken links found by the weekly check."},
	{"docs", 84, "Rewrite the quickstart", "priya", "docs/quickstart", 287, 341, "commented", "Shorter quickstart: install, first dictation and shortcuts on one page."},
	{"docs", 81, "API reference for organizations", "chris", "docs/org-api", 532, 18, "draft", "Reference pages for the organization endpoints."},
}

// demoReviewers pairs each author with someone else to review.
var demoReviewers = []string{"mike", "sarah", "alex", "jordan", "chris", "taylor"}

func (d demoPR) toPR(i int) PR {
	var pr PR
	pr.Number, pr.Title, pr.HeadRefName = d.number, d.title, d.branch
	pr.Additions, pr.Deletions = d.adds, d.dels
	pr.Author.Login = d.author
	pr.Repository.Name, pr.Repository.Owner.Login = d.repo, "acme-corp"
	pr.HeadRefOid = fmt.Sprintf("%07x", d.number*7919)
	pr.UpdatedAt = demoNow.Add(-time.Duration(i*47) * time.Minute)
	reviewer := demoReviewers[i%len(demoReviewers)]
	if reviewer == d.author {
		reviewer = demoReviewers[(i+1)%len(demoReviewers)]
	}
	review := func(state string) {
		pr.Reviews.Nodes = append(pr.Reviews.Nodes, struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State string `json:"state"`
		}{State: state})
		pr.Reviews.Nodes[0].Author.Login = reviewer
	}
	request := func() {
		pr.ReviewRequests.TotalCount = 1
		pr.ReviewRequests.Nodes = make([]struct {
			RequestedReviewer struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			} `json:"requestedReviewer"`
		}, 1)
		pr.ReviewRequests.Nodes[0].RequestedReviewer.Login = reviewer
	}
	switch d.state {
	case "approved":
		pr.ReviewDecision = "APPROVED"
		review("APPROVED")
	case "denied":
		pr.ReviewDecision = "CHANGES_REQUESTED"
		review("CHANGES_REQUESTED")
	case "commented":
		pr.ReviewDecision = "REVIEW_REQUIRED"
		review("COMMENTED")
		request()
	case "review":
		pr.ReviewDecision = "REVIEW_REQUIRED"
		request()
	case "draft":
		pr.IsDraft = true
	}
	return pr
}

func findDemoPR(pr PR) (demoPR, bool) {
	for _, d := range demoPRs {
		if d.number == pr.Number && d.repo == pr.Repository.Name {
			return d, true
		}
	}
	return demoPR{}, false
}

// demoPrefill puts every PR's detail and diff in the cache, so the demo never shows loading.
func (m *model) demoPrefill() {
	for _, pr := range m.prs {
		e := m.cacheEntry(prKey(pr))
		if e.detail == nil {
			e.detail, e.detailAt = mockPRDetail(pr), time.Now()
		}
		if e.files == nil {
			e.patch, e.head, e.hasPatch = mockPatch, pr.HeadRefOid, true
			e.files = parseUnifiedDiff(mockPatch)
			highlightFiles(e.files)
		}
	}
}

// demoActions holds what was done to each PR during the demo (prKey -> action), so it sticks.
var demoActions sync.Map

func demoAction(pr PR) string {
	a, _ := demoActions.Load(prKey(pr))
	s, _ := a.(string)
	return s
}

// demoGone reports a PR merged or closed during the demo, so refreshes leave it out.
func demoGone(pr PR) bool {
	a := demoAction(pr)
	return a == "merged" || a == "closed"
}

func applyDemoAction(pr PR) PR {
	switch a := demoAction(pr); a {
	case "draft", "ready":
		pr.IsDraft = a == "draft"
	case "approve", "request-changes":
		state := map[string]string{"approve": "APPROVED", "request-changes": "CHANGES_REQUESTED"}[a]
		pr.ReviewDecision, pr.IsDraft = state, false
		pr.ReviewRequests.TotalCount, pr.ReviewRequests.Nodes = 0, nil
		pr.Reviews.Nodes = pr.Reviews.Nodes[:0:0]
		pr.Reviews.Nodes = append(pr.Reviews.Nodes, struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State string `json:"state"`
		}{State: state})
		pr.Reviews.Nodes[0].Author.Login = currentUser
	}
	return pr
}
