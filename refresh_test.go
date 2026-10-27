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
	if got := numbers(m.prs); len(got) != 4 || got[3] != 9 {
		t.Fatalf("want [1 2 4 9], got %v", got)
	}
	if !m.prs[3].Partial || statusKind(m.prs[3]) != statusPending {
		t.Fatalf("new PR should render as pending until its row loads")
	}
	if m.pendingRows != 1 || cmd == nil {
		t.Fatalf("want one row batch for the two changed PRs, got %d", m.pendingRows)
	}

	updated := m.prs[1]
	updated.Title, updated.UpdatedAt = "updated", now.Add(time.Minute)
	m.handleRowsRefreshed(prsRefreshedMsg{refreshID: m.refreshID, prs: []PR{updated, newPR}})
	if m.refreshing || m.pendingRows != 0 || m.lastRefresh.IsZero() {
		t.Fatalf("refresh should finish after the last batch")
	}
	if m.prs[3].Partial || m.prs[1].Title != "updated" {
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
