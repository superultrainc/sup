package main

import (
	"testing"
	"time"
)

func TestWarmQueueOrderAndSkip(t *testing.T) {
	m := sampleModel()
	m.cache = map[string]*prCacheEntry{}
	now := time.Now()
	for i := range m.prs {
		m.prs[i].UpdatedAt = now
		m.prs[i].HeadRefOid = "head"
	}
	m.filtered = m.prs
	m.cursor = 2

	// PR #4 is already fresh in memory, so it must be skipped.
	fresh := &prDetail{UpdatedAt: now}
	m.cache[prKey(m.prs[3])] = &prCacheEntry{detail: fresh, hasPatch: true, head: "head"}

	m.enqueueWarm()
	// Four PRs, one already fresh: three warm, nothing left queued.
	if m.warmInFlight != 3 || len(m.warmQueue) != 0 {
		t.Fatalf("want 3 in flight and none queued, got %d in flight, %d queued", m.warmInFlight, len(m.warmQueue))
	}

	// With every slot busy, PRs stay queued nearest-cursor first.
	m.warmInFlight, m.warmQueue = warmConcurrency, nil
	m.cursor = 0
	m.enqueueWarm()
	if len(m.warmQueue) != len(m.prs) || m.warmQueue[0].Number != m.filtered[0].Number {
		t.Fatalf("queue should hold every PR starting at the cursor, got %d", len(m.warmQueue))
	}
}

func TestWarmSnapshotNeeds(t *testing.T) {
	now := time.Now()
	pr := PR{UpdatedAt: now, HeadRefOid: "abc"}
	if d, p := (warmSnapshot{detailUpdated: now, head: "abc"}).needs(pr); d || p {
		t.Errorf("matching snapshot should need nothing, got detail=%v patch=%v", d, p)
	}
	if d, p := (warmSnapshot{detailUpdated: now.Add(-time.Minute), head: "old"}).needs(pr); !d || !p {
		t.Errorf("stale snapshot should need both, got detail=%v patch=%v", d, p)
	}
}
