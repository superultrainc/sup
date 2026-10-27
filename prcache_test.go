package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func cacheTestModel() (model, PR) {
	m := sampleModel()
	m.cache = map[string]*prCacheEntry{}
	m.width, m.height = 120, 40
	pr := m.prs[0]
	pr.UpdatedAt, pr.HeadRefOid = time.Now(), "head"
	m.prs[0], m.filtered = pr, m.prs
	return m, pr
}

// Warming a PR that isn't on screen must not mark its images as loading,
// or hover/open would never download them.
func TestWarmDoesNotStrandImages(t *testing.T) {
	m, pr := cacheTestModel()
	url := "https://github.com/user-attachments/assets/warm-test"
	delete(imageStore, url)
	d := &prDetail{UpdatedAt: pr.UpdatedAt, Body: "![shot](" + url + ")"}
	m.handleWarmDone(warmDoneMsg{pr: pr, detail: &prDetailFetchedMsg{key: prKey(pr), detail: d, fp: "x"}})
	if _, ok := imageStore[url]; ok {
		t.Fatalf("warming queued (and stranded) an image download")
	}
	if cmd := queueImages(d, false); cmd == nil {
		t.Fatalf("opening later should still queue the image")
	}
}

// While a warm is in flight, hover and open must wait for it instead of refetching.
func TestWarmBlocksDuplicateFetches(t *testing.T) {
	m, pr := cacheTestModel()
	m.warmQueue = []PR{pr}
	m.pumpWarm()
	if cmds := m.fetchMissing(pr, m.cacheEntry(prKey(pr))); len(cmds) != 0 {
		t.Fatalf("fetchMissing started %d duplicate fetches during a warm", len(cmds))
	}
}

// A warmed (raw) patch is parsed when the PR opens.
func TestOpenParsesRawPatch(t *testing.T) {
	m, pr := cacheTestModel()
	e := m.cacheEntry(prKey(pr))
	e.detail, e.detailAt = &prDetail{UpdatedAt: pr.UpdatedAt}, time.Now()
	e.patch, e.head, e.hasPatch = mockPatch, "head", true

	cmd := m.openDetail(pr)
	if m.detail.diff != nil || !m.detail.diffLoading {
		t.Fatalf("diff should be loading until parsed")
	}
	var parsed *patchParsedMsg
	for _, msg := range runCmds(cmd) {
		if p, ok := msg.(patchParsedMsg); ok {
			parsed = &p
		}
	}
	if parsed == nil {
		t.Fatalf("opening didn't parse the cached patch")
	}
	m.handlePatchParsed(*parsed)
	if len(m.detail.diff) != 4 || m.detail.diffLoading {
		t.Fatalf("parsed diff not shown: %d files, loading=%v", len(m.detail.diff), m.detail.diffLoading)
	}
}

// runCmds executes a command tree, skipping ticks and network fetches.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, runCmds(c)...)
			}
			return out
		}
		return []tea.Msg{msg}
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}
