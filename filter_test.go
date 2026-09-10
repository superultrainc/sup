package main

import (
	"strings"
	"testing"
)

func testPR(repo, author string, num int, draft bool, decision string, reviewers ...string) PR {
	pr := PR{Number: num, IsDraft: draft, ReviewDecision: decision}
	pr.Repository.Name = repo
	pr.Repository.Owner.Login = "superultrainc"
	pr.Author.Login = author
	for _, r := range reviewers {
		var node struct {
			RequestedReviewer struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			} `json:"requestedReviewer"`
		}
		node.RequestedReviewer.Login = r
		pr.ReviewRequests.Nodes = append(pr.ReviewRequests.Nodes, node)
		pr.ReviewRequests.TotalCount++
	}
	return pr
}

func sampleModel() model {
	prs := []PR{
		testPR("superwhisper-ios", "neil", 1, false, "", "max"),
		testPR("superwhisper-ios", "max", 2, true, ""),
		testPR("sup", "neil", 3, false, "APPROVED", "ben"),
		testPR("superwhisper-api", "ben", 4, false, "", "neil"),
	}
	return model{prs: prs, filtered: prs, statusFilterIndex: -1, repoFilterIndex: -1, repos: buildRepoList(prs)}
}

func numbers(prs []PR) []int {
	out := make([]int, len(prs))
	for i, pr := range prs {
		out[i] = pr.Number
	}
	return out
}

func assertPRs(t *testing.T, got []PR, want ...int) {
	t.Helper()
	g := numbers(got)
	if len(g) != len(want) {
		t.Fatalf("got PRs %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got PRs %v, want %v", g, want)
		}
	}
}

func TestBuildRepoListIsSortedAndUnique(t *testing.T) {
	m := sampleModel()
	want := []string{"sup", "superwhisper-api", "superwhisper-ios"}
	if strings.Join(m.repos, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", m.repos, want)
	}
}

func TestRepoCycleVisitsEveryRepoThenClears(t *testing.T) {
	m := sampleModel()

	m.cycleRepoFilter(1)
	assertPRs(t, m.filtered, 3) // sup

	m.cycleRepoFilter(1)
	assertPRs(t, m.filtered, 4) // superwhisper-api

	m.cycleRepoFilter(1)
	assertPRs(t, m.filtered, 1, 2) // superwhisper-ios

	m.cycleRepoFilter(1) // wraps back to unfiltered
	if m.repoFilterIndex != -1 {
		t.Fatalf("expected filter cleared, got index %d", m.repoFilterIndex)
	}
	assertPRs(t, m.filtered, 1, 2, 3, 4)
}

func TestRepoCycleBackwards(t *testing.T) {
	m := sampleModel()
	m.cycleRepoFilter(-1)
	if got := m.currentRepo(); got != "superwhisper-ios" {
		t.Fatalf("got %q, want superwhisper-ios", got)
	}
	m.cycleRepoFilter(-1)
	if got := m.currentRepo(); got != "superwhisper-api" {
		t.Fatalf("got %q, want superwhisper-api", got)
	}
}

func TestRepoCycleNoopWithoutRepos(t *testing.T) {
	m := model{statusFilterIndex: -1, repoFilterIndex: -1}
	m.cycleRepoFilter(1)
	if m.repoFilterIndex != -1 {
		t.Fatalf("expected no filter, got index %d", m.repoFilterIndex)
	}
}

func TestHashPrefixFiltersByRepo(t *testing.T) {
	m := sampleModel()
	m.filterText = "#superwhisper-ios"
	m.applyFilter()
	assertPRs(t, m.filtered, 1, 2)
}

func TestHashPrefixIsExactNotSubstring(t *testing.T) {
	m := sampleModel()
	m.filterText = "#superwhisper"
	m.applyFilter()
	assertPRs(t, m.filtered)
}

func TestRepoComposesWithAuthorAndStatus(t *testing.T) {
	m := sampleModel()
	m.repoFilterIndex = 2 // superwhisper-ios
	m.authorFilter = "!max"
	m.statusFilterIndex = indexOfStatus("draft")
	m.applyFilter()
	assertPRs(t, m.filtered, 2)

	// Same repo + author, but a status no PR in it has.
	m.statusFilterIndex = indexOfStatus("approved")
	m.applyFilter()
	assertPRs(t, m.filtered)
}

// Author-only and status-only filtering were no-ops before applyFilter was
// rewritten to AND the dimensions together.
func TestAuthorFilterAloneFilters(t *testing.T) {
	m := sampleModel()
	m.authorFilter = "!neil"
	m.applyFilter()
	assertPRs(t, m.filtered, 1, 3)
}

func TestStatusFilterAloneFilters(t *testing.T) {
	m := sampleModel()
	m.statusFilterIndex = indexOfStatus("draft")
	m.applyFilter()
	assertPRs(t, m.filtered, 2)
}

func TestReviewerFilterAloneFilters(t *testing.T) {
	m := sampleModel()
	m.authorFilter = "@neil"
	m.applyFilter()
	assertPRs(t, m.filtered, 4)
}

func TestStatusCycleWrapsThroughUnfiltered(t *testing.T) {
	m := sampleModel()
	for i := range statusFilters {
		m.cycleStatusFilter()
		if m.statusFilterIndex != i {
			t.Fatalf("step %d: got index %d, want %d", i, m.statusFilterIndex, i)
		}
	}
	m.cycleStatusFilter()
	if m.statusFilterIndex != -1 {
		t.Fatalf("expected cycle to clear, got %d", m.statusFilterIndex)
	}
	assertPRs(t, m.filtered, 1, 2, 3, 4)
}

func TestTypedStatusNameDrivesStatusCycle(t *testing.T) {
	m := sampleModel()
	m.filterText = "draft"
	m.applyFilter()
	if m.statusFilterIndex != indexOfStatus("draft") {
		t.Fatalf("typed status did not set cycle index, got %d", m.statusFilterIndex)
	}
	assertPRs(t, m.filtered, 2)
}

func TestFreeTextStillSearchesAllFields(t *testing.T) {
	m := sampleModel()
	m.filterText = "superwhisper-api"
	m.applyFilter()
	assertPRs(t, m.filtered, 4)
}

func TestSyncRepoListKeepsPinnedRepoAcrossRefresh(t *testing.T) {
	m := sampleModel()
	m.repoFilterIndex = 2 // superwhisper-ios
	// A refresh drops "sup", shifting superwhisper-ios down an index.
	m.prs = []PR{
		testPR("superwhisper-api", "ben", 4, false, ""),
		testPR("superwhisper-ios", "neil", 1, false, ""),
	}
	m.syncRepoList()
	if got := m.currentRepo(); got != "superwhisper-ios" {
		t.Fatalf("pinned repo lost: got %q", got)
	}
}

func TestSyncRepoListClearsWhenPinnedRepoDisappears(t *testing.T) {
	m := sampleModel()
	m.repoFilterIndex = 0 // sup
	m.prs = []PR{testPR("superwhisper-ios", "neil", 1, false, "")}
	m.syncRepoList()
	if m.repoFilterIndex != -1 {
		t.Fatalf("expected filter cleared, got index %d", m.repoFilterIndex)
	}
}
