package main

import (
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Warming fetches every listed PR's detail and diff in the background, nearest
// the cursor first, so opening a PR rarely waits on the network. A PR is skipped
// when its cached detail matches the list's updatedAt and its diff the head commit.

const warmConcurrency = 6

// warmSnapshot is what the cache already holds, taken on the UI goroutine.
type warmSnapshot struct {
	inMemory      bool
	detailUpdated time.Time
	head          string
}

func (s warmSnapshot) needs(pr PR) (detail, patch bool) {
	detail = s.detailUpdated.IsZero() || !s.detailUpdated.Equal(pr.UpdatedAt)
	patch = s.head == "" || s.head != pr.HeadRefOid
	return
}

type warmDoneMsg struct {
	pr     PR
	disk   *diskCacheLoadedMsg
	detail *prDetailFetchedMsg
	patch  *prPatchFetchedMsg
}

func warmCmd(pr PR, snap warmSnapshot) tea.Cmd {
	return func() tea.Msg {
		out := warmDoneMsg{pr: pr}
		if !snap.inMemory {
			if dm, ok := readDiskCache(pr, false); ok {
				out.disk = &dm
				if dm.detail != nil {
					snap.detailUpdated = dm.detail.UpdatedAt
				}
				snap.head = dm.head
			}
		}
		needDetail, needPatch := snap.needs(pr)
		var wg sync.WaitGroup
		if needDetail {
			wg.Add(1)
			go func() {
				defer wg.Done()
				msg := fetchPRDetailCmd(pr)().(prDetailFetchedMsg)
				out.detail = &msg
			}()
		}
		if needPatch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				msg := fetchPatchCmd(pr, false)().(prPatchFetchedMsg)
				out.patch = &msg
			}()
		}
		wg.Wait()
		return out
	}
}

// enqueueWarm queues every PR, ordered by distance from the cursor.
func (m *model) enqueueWarm() tea.Cmd {
	if demoMode {
		return nil
	}
	pos := map[string]int{}
	for i, pr := range m.filtered {
		pos[prKey(pr)] = i
	}
	queue := append([]PR(nil), m.prs...)
	dist := func(pr PR) int {
		if i, ok := pos[prKey(pr)]; ok {
			return abs(i - m.cursor)
		}
		return len(m.prs) + 1 // filtered out: after everything visible
	}
	sort.SliceStable(queue, func(i, j int) bool { return dist(queue[i]) < dist(queue[j]) })
	m.warmQueue = queue
	return m.pumpWarm()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// pumpWarm starts queued warms up to the concurrency limit, skipping PRs
// already fresh in memory or being fetched by an open view or hover.
func (m *model) pumpWarm() tea.Cmd {
	var cmds []tea.Cmd
	for m.warmInFlight < warmConcurrency && len(m.warmQueue) > 0 {
		pr := m.warmQueue[0]
		m.warmQueue = m.warmQueue[1:]
		e, inMemory := m.cache[prKey(pr)]
		snap := warmSnapshot{inMemory: inMemory}
		if inMemory {
			if e.detailPending || e.patchPending {
				continue
			}
			if e.detail != nil {
				snap.detailUpdated = e.detail.UpdatedAt
			}
			if e.hasPatch {
				snap.head = e.head
			}
			if d, p := snap.needs(pr); !d && !p {
				continue
			}
		}
		// Mark both in flight so hover and open wait for this instead of refetching.
		e = m.cacheEntry(prKey(pr))
		e.detailPending, e.patchPending = true, true
		m.warmInFlight++
		cmds = append(cmds, warmCmd(pr, snap))
	}
	return tea.Batch(cmds...)
}

// handleWarmDone merges results through the regular cache handlers. Images
// are only queued for the PR on screen; others load on hover or open.
func (m *model) handleWarmDone(msg warmDoneMsg) tea.Cmd {
	m.warmInFlight--
	key := prKey(msg.pr)
	open := m.detail != nil && prKey(m.detail.pr) == key
	e := m.cacheEntry(key)
	e.detailPending, e.patchPending = false, false
	var cmds []tea.Cmd
	if msg.disk != nil {
		cmds = append(cmds, m.handleDiskCacheLoaded(*msg.disk, open))
	}
	if msg.detail != nil {
		cmds = append(cmds, m.handleDetailFetched(*msg.detail, open))
	}
	if msg.patch != nil {
		cmds = append(cmds, m.handlePatchFetched(*msg.patch))
	}
	if open {
		// The view may have opened mid-warm and be waiting on these.
		v := m.detail
		v.loading = v.detail == nil
		if v.diff == nil {
			cmds = append(cmds, m.ensureParsed(key, e))
		}
	}
	cmds = append(cmds, m.pumpWarm())
	return tea.Batch(cmds...)
}
