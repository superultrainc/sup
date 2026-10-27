package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// prCacheEntry holds what the PR view needs so it can open instantly and
// revalidate in the background. Only touched from Update.
type prCacheEntry struct {
	detail        *prDetail
	detailJSON    []byte
	detailAt      time.Time
	detailPending bool

	files        []*diffFile
	patch        string
	head         string // headRefOid the patch was fetched at
	patchPending bool
}

const (
	hoverDelay       = 150 * time.Millisecond
	detailFreshFor   = 30 * time.Second // skip revalidating right after a prefetch
	diskCacheMaxAge  = 14 * 24 * time.Hour
	prCacheDirName   = "prs"
	prCacheExtension = ".json"
)

func (e *prCacheEntry) patchFresh(pr PR) bool {
	return e.files != nil && pr.HeadRefOid != "" && e.head == pr.HeadRefOid
}

func (m *model) cacheEntry(key string) *prCacheEntry {
	e, ok := m.cache[key]
	if !ok {
		e = &prCacheEntry{}
		m.cache[key] = e
	}
	return e
}

// fetchMissing starts whichever fetches the cached entry needs, skipping ones in flight.
func (m *model) fetchMissing(pr PR, e *prCacheEntry) []tea.Cmd {
	var cmds []tea.Cmd
	if !e.detailPending && (e.detail == nil || time.Since(e.detailAt) > detailFreshFor) {
		e.detailPending = true
		cmds = append(cmds, fetchPRDetailCmd(pr))
	}
	if !e.patchPending && !e.patchFresh(pr) {
		e.patchPending = true
		cmds = append(cmds, fetchPatchCmd(pr))
	}
	return cmds
}

// openDetail shows cached data immediately and revalidates what's stale.
func (m *model) openDetail(pr PR) tea.Cmd {
	key := prKey(pr)
	v := newDetailView(pr)
	v.width = m.width
	_, inMemory := m.cache[key]
	e := m.cacheEntry(key)
	v.detail, v.diff = e.detail, e.files
	v.loading = e.detailPending || e.detail == nil || time.Since(e.detailAt) > detailFreshFor
	v.diffLoading = e.patchPending || !e.patchFresh(pr)
	m.detail = v
	if v.detail != nil || v.diff != nil {
		v.rebuild(m.width)
	}
	cmds := append(m.fetchMissing(pr, e), spinnerTick(), queueImages(e.detail, false))
	if !inMemory {
		cmds = append(cmds, loadDiskCacheCmd(pr))
	}
	return tea.Batch(cmds...)
}

type hoverMsg struct {
	seq int
	pr  PR
}

// schedulePrefetch prefetches the cursor's PR once it rests for hoverDelay.
func (m *model) schedulePrefetch() tea.Cmd {
	pr, ok := m.currentPR()
	if !ok {
		return nil
	}
	m.hoverSeq++
	seq := m.hoverSeq
	return tea.Tick(hoverDelay, func(time.Time) tea.Msg { return hoverMsg{seq: seq, pr: pr} })
}

func (m *model) handleHover(msg hoverMsg) tea.Cmd {
	if msg.seq != m.hoverSeq || m.detail != nil {
		return nil
	}
	key := prKey(msg.pr)
	_, inMemory := m.cache[key]
	cmds := m.fetchMissing(msg.pr, m.cacheEntry(key))
	if !inMemory {
		cmds = append(cmds, loadDiskCacheCmd(msg.pr))
	}
	return tea.Batch(cmds...)
}

// bodyHTMLRe matches bodyHTML JSON fields, whose signed image URLs change on every fetch.
var bodyHTMLRe = regexp.MustCompile(`"bodyHTML":"(?:[^"\\]|\\.)*"`)

func (m *model) handleDetailFetched(msg prDetailFetchedMsg) tea.Cmd {
	e := m.cacheEntry(msg.key)
	e.detailPending = false
	changed := false
	var images tea.Cmd
	if msg.err == nil {
		raw, _ := json.Marshal(msg.detail)
		changed = !bytes.Equal(bodyHTMLRe.ReplaceAll(raw, nil), bodyHTMLRe.ReplaceAll(e.detailJSON, nil))
		e.detail, e.detailJSON, e.detailAt = msg.detail, raw, time.Now()
		if changed {
			saveDiskCache(msg.key, e)
		}
		// Fresh signed URLs: download now, even on a hover prefetch.
		images = queueImages(msg.detail, true)
	}
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return images
	}
	v.loading = false
	v.err = msg.err
	if msg.err == nil && (changed || v.detail == nil) {
		v.detail = msg.detail
		v.rebuild(m.width)
		v.clampScroll(m.height)
	}
	return images
}

func (m *model) handlePatchFetched(msg prPatchFetchedMsg) {
	e := m.cacheEntry(msg.key)
	e.patchPending = false
	changed := false
	if msg.err == nil {
		changed = msg.patch != e.patch
		e.files, e.patch, e.head = msg.files, msg.patch, msg.head
		if changed {
			saveDiskCache(msg.key, e)
		}
	}
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return
	}
	v.diffLoading = false
	v.diffErr = msg.err
	if msg.err == nil && (changed || v.diff == nil) {
		v.diff = msg.files
		v.rebuildFilesKeepingPlace()
		v.clampScroll(m.height)
	}
}

type diskCacheEntry struct {
	Detail   json.RawMessage `json:"detail,omitempty"`
	DetailAt time.Time       `json:"detailAt"`
	Patch    string          `json:"patch,omitempty"`
	Head     string          `json:"head,omitempty"`
}

func diskCachePath(key string) string {
	name := strings.NewReplacer("/", "_", "#", "_").Replace(key)
	return filepath.Join(cacheDir(), prCacheDirName, name+prCacheExtension)
}

func saveDiskCache(key string, e *prCacheEntry) {
	if demoMode {
		return
	}
	data, err := json.Marshal(diskCacheEntry{Detail: e.detailJSON, DetailAt: e.detailAt, Patch: e.patch, Head: e.head})
	if err != nil {
		return
	}
	path := diskCachePath(key)
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, data, 0644)
}

type diskCacheLoadedMsg struct {
	key      string
	detail   *prDetail
	raw      []byte
	detailAt time.Time
	files    []*diffFile
	patch    string
	head     string
}

// loadDiskCacheCmd parses and highlights a cached PR off the UI goroutine.
func loadDiskCacheCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		key := prKey(pr)
		data, err := os.ReadFile(diskCachePath(key))
		if err != nil || demoMode {
			return nil
		}
		var de diskCacheEntry
		if json.Unmarshal(data, &de) != nil {
			return nil
		}
		msg := diskCacheLoadedMsg{key: key, raw: de.Detail, detailAt: de.DetailAt, patch: de.Patch, head: de.Head}
		if len(de.Detail) > 0 {
			var d prDetail
			if json.Unmarshal(de.Detail, &d) == nil {
				msg.detail = &d
			}
		}
		if de.Patch != "" {
			msg.files = parseUnifiedDiff(de.Patch)
			highlightFiles(msg.files)
		}
		return msg
	}
}

// handleDiskCacheLoaded fills gaps only; network results always win.
func (m *model) handleDiskCacheLoaded(msg diskCacheLoadedMsg) tea.Cmd {
	e := m.cacheEntry(msg.key)
	if e.detail == nil && msg.detail != nil {
		e.detail, e.detailJSON, e.detailAt = msg.detail, msg.raw, msg.detailAt
	}
	if e.files == nil && msg.files != nil {
		e.files, e.patch, e.head = msg.files, msg.patch, msg.head
	}
	// Cached signed URLs have likely expired, but the image disk cache may have them.
	images := queueImages(e.detail, false)
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return images
	}
	rebuilt := false
	if v.detail == nil && e.detail != nil {
		v.detail = e.detail
		rebuilt = true
	}
	if v.diff == nil && e.files != nil {
		v.diff = e.files
		rebuilt = true
	}
	if rebuilt {
		v.rebuild(m.width)
		v.clampScroll(m.height)
	}
	return images
}

// pruneDiskCache drops cached PRs nobody has opened in a while.
func pruneDiskCache() {
	dir := filepath.Join(cacheDir(), prCacheDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if info, err := de.Info(); err == nil && time.Since(info.ModTime()) > diskCacheMaxAge {
			os.Remove(filepath.Join(dir, de.Name()))
		}
	}
}
