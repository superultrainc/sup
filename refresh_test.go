package main

import (
	"testing"
	"time"
)

func stubFor(pr PR, updated time.Time) prStub {
	var s prStub
	s.Number, s.UpdatedAt, s.HeadRefOid = pr.Number, updated, pr.HeadRefOid
	s.Repository.Name, s.Repository.Owner.Login = pr.Repository.Name, pr.Repository.Owner.Login
	return s
}

func TestRefreshFetchesOnlyChangedRows(t *testing.T) {
	m := sampleModel()
	m.cache = map[string]*prCacheEntry{}
	now := time.Now()
	for i := range m.prs {
		m.prs[i].UpdatedAt = now
	}
	// #1 unchanged, #2 updated, #3 closed (missing), #4 unchanged, plus a new #9.
	newPR := testPR("sup", "max", 9, false, "")
	discovered := []prStub{
		stubFor(m.prs[0], now),
		stubFor(m.prs[1], now.Add(time.Minute)),
		stubFor(m.prs[3], now),
		stubFor(newPR, now),
	}

	orgs = []string{"superultrainc"}
	m.startRefresh()
	cmd := m.handleDiscovered(discoveredMsg{refreshID: m.refreshID, stubs: discovered})

	// Closed #3 is pruned and new #9 shows up immediately, as a partial row.
	byNum := func(n int) *PR {
		for i := range m.prs {
			if m.prs[i].Number == n {
				return &m.prs[i]
			}
		}
		return nil
	}
	if got := numbers(m.prs); len(got) != 4 || byNum(3) != nil || byNum(9) == nil {
		t.Fatalf("want 1, 2, 4, 9, got %v", got)
	}
	if !byNum(9).Partial || statusKind(*byNum(9)) != statusPending {
		t.Fatalf("new PR should render as pending until its row loads")
	}
	if m.pendingRows != 1 || cmd == nil {
		t.Fatalf("want one row batch for the two changed PRs, got %d", m.pendingRows)
	}

	updated := *byNum(2)
	updated.Title, updated.UpdatedAt = "updated", now.Add(time.Minute)
	m.handleRowsRefreshed(prsRefreshedMsg{refreshID: m.refreshID, prs: []PR{updated, newPR}})
	if m.refreshing || m.pendingRows != 0 || m.lastRefresh.IsZero() {
		t.Fatalf("refresh should finish after the last batch")
	}
	if byNum(9).Partial || byNum(2).Title != "updated" {
		t.Fatalf("rows should be filled in: %+v", m.prs)
	}
}

// A failed shard must not prune PRs it simply didn't return.
func TestRefreshKeepsListWhenDiscoveryFails(t *testing.T) {
	m := sampleModel()
	orgs = []string{"superultrainc", "other"}
	m.startRefresh()
	m.handleDiscovered(discoveredMsg{refreshID: m.refreshID, stubs: []prStub{stubFor(m.prs[0], time.Time{})}})
	m.handleDiscovered(discoveredMsg{refreshID: m.refreshID, err: errTest})
	if len(m.prs) != 4 {
		t.Fatalf("partial discovery pruned the list to %d", len(m.prs))
	}
}

var errTest = &testError{}

type testError struct{}

func (*testError) Error() string { return "boom" }

func TestSortDraftsLastThenRecentlyUpdated(t *testing.T) {
	now := time.Now()
	old, fresh, draft := testPR("a", "x", 1, false, ""), testPR("b", "x", 2, false, ""), testPR("a", "x", 3, false, "")
	old.UpdatedAt, fresh.UpdatedAt, draft.UpdatedAt = now.Add(-time.Hour), now, now.Add(time.Hour)
	draft.IsDraft = true
	prs := []PR{draft, old, fresh}
	sortPRs(prs)
	if got := numbers(prs); got[0] != 2 || got[1] != 1 || got[2] != 3 {
		t.Fatalf("want [2 1 3], got %v", got)
	}
}
