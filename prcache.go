package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// prCacheEntry holds what the PR view needs so it can open instantly and
// revalidate in the background. Only touched from Update.
type prCacheEntry struct {
	detail        *prDetail
	detailJSON    []byte
	detailFP      string // detailJSON minus bodyHTML, for change detection
	detailAt      time.Time
	detailPending bool

	patch        string // raw unified diff
	hasPatch     bool
	head         string // headRefOid the patch was fetched at
	patchPending bool
	// files is the parsed, highlighted patch. Warming keeps only the raw patch;
	// it's parsed when a PR is hovered or opened (see ensureParsed).
	files   []*diffFile
	parsing bool
}

const (
	hoverDelay     = 150 * time.Millisecond
	detailFreshFor = 30 * time.Second // skip revalidating right after a prefetch
	// detailTrustUnchanged bounds how long a detail whose updatedAt still matches
	// the list counts as fresh; checks and mergeability change without bumping it.
	detailTrustUnchanged = 5 * time.Minute
	diskCacheMaxAge      = 14 * 24 * time.Hour
	prCacheDirName       = "prs"
	prCacheExtension     = ".json"
)

func (e *prCacheEntry) detailFresh(pr PR) bool {
	if e.detail == nil {
		return false
	}
	age := time.Since(e.detailAt)
	if age < detailFreshFor {
		return true
	}
	return !pr.UpdatedAt.IsZero() && e.detail.UpdatedAt.Equal(pr.UpdatedAt) && age < detailTrustUnchanged
}

func (e *prCacheEntry) patchFresh(pr PR) bool {
	return e.hasPatch && pr.HeadRefOid != "" && e.head == pr.HeadRefOid
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
	if !e.detailPending && !e.detailFresh(pr) {
		e.detailPending = true
		cmds = append(cmds, fetchPRDetailCmd(pr))
	}
	if !e.patchPending && !e.patchFresh(pr) {
		e.patchPending = true
		cmds = append(cmds, fetchPatchCmd(pr, true))
	}
	return cmds
}

type patchParsedMsg struct {
	key   string
	patch string
	files []*diffFile
}

// ensureParsed parses and highlights a raw cached patch off the UI goroutine.
func (m *model) ensureParsed(key string, e *prCacheEntry) tea.Cmd {
	if e.files != nil || !e.hasPatch || e.parsing {
		return nil
	}
	e.parsing = true
	patch := e.patch
	return func() tea.Msg {
		files := parseUnifiedDiff(patch)
		highlightFiles(files)
		return patchParsedMsg{key: key, patch: patch, files: files}
	}
}

func (m *model) handlePatchParsed(msg patchParsedMsg) tea.Cmd {
	e := m.cacheEntry(msg.key)
	e.parsing = false
	if msg.patch != e.patch {
		// A newer patch arrived while parsing; parse that one instead.
		return m.ensureParsed(msg.key, e)
	}
	e.files = msg.files
	if v := m.detail; v != nil && prKey(v.pr) == msg.key {
		v.diff = e.files
		v.diffLoading = e.patchPending
		v.invalidateFiles()
	}
	return nil
}

// openDetail shows cached data immediately and revalidates what's stale.
func (m *model) openDetail(pr PR) tea.Cmd {
	key := prKey(pr)
	v := newDetailView(pr)
	v.width = m.width
	_, inMemory := m.cache[key]
	e := m.cacheEntry(key)
	v.detail, v.diff = e.detail, e.files
	v.loading = e.detailPending || !e.detailFresh(pr)
	v.diffLoading = e.patchPending || !e.patchFresh(pr) || e.files == nil
	m.detail = v
	if v.detail != nil || v.diff != nil {
		v.rebuild(m.width)
	}
	cmds := append(m.fetchMissing(pr, e), spinnerTick(), m.ensureParsed(key, e), queueImages(e.detail, false))
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

// handleHover fetches what's stale and parses the diff, since an open is likely next.
func (m *model) handleHover(msg hoverMsg) tea.Cmd {
	if msg.seq != m.hoverSeq || m.detail != nil {
		return nil
	}
	key := prKey(msg.pr)
	_, inMemory := m.cache[key]
	e := m.cacheEntry(key)
	cmds := append(m.fetchMissing(msg.pr, e), m.ensureParsed(key, e))
	if m.previewActive() {
		cmds = append(cmds, queueImages(e.detail, false)) // so screenshots fill in while previewing
	}
	if !inMemory {
		cmds = append(cmds, loadDiskCacheCmd(msg.pr))
	}
	return tea.Batch(cmds...)
}

// bodyHTMLRe matches bodyHTML JSON fields, whose signed image URLs change on every fetch.
var bodyHTMLRe = regexp.MustCompile(`"bodyHTML":"(?:[^"\\]|\\.)*"`)

func detailFingerprint(raw []byte) string {
	return string(bodyHTMLRe.ReplaceAll(raw, nil))
}

// handleDetailFetched stores a fetched detail. wantImages is false for
// background warming, whose image downloads would otherwise be queued and dropped.
func (m *model) handleDetailFetched(msg prDetailFetchedMsg, wantImages bool) tea.Cmd {
	e := m.cacheEntry(msg.key)
	e.detailPending = false
	changed := false
	var images tea.Cmd
	if msg.err == nil {
		changed = msg.fp != e.detailFP
		e.detail, e.detailJSON, e.detailFP, e.detailAt = msg.detail, msg.raw, msg.fp, time.Now()
		if changed {
			saveDiskCache(msg.key, e)
		}
		if wantImages {
			// Fresh signed URLs: download now, even on a hover prefetch.
			images = queueImages(msg.detail, true)
		}
	}
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return images
	}
	v.loading = false
	v.err = msg.err
	if msg.err == nil && (changed || v.detail == nil) {
		v.applyDetail(msg.detail)
		v.clampScroll(m.height)
	}
	return images
}

func (m *model) handlePatchFetched(msg prPatchFetchedMsg) tea.Cmd {
	e := m.cacheEntry(msg.key)
	e.patchPending = false
	changed := false
	if msg.err == nil {
		changed = !e.hasPatch || msg.patch != e.patch
		e.patch, e.head, e.hasPatch = msg.patch, msg.head, true
		switch {
		case msg.files != nil:
			e.files = msg.files
		case changed:
			e.files = nil // stale parse; reparsed on demand
		}
		if changed {
			saveDiskCache(msg.key, e)
		}
	}
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return nil
	}
	v.diffErr = msg.err
	if msg.err != nil {
		v.diffLoading = false
		return nil
	}
	if e.files == nil {
		return m.ensureParsed(msg.key, e)
	}
	v.diffLoading = false
	if changed || v.diff == nil {
		v.diff = e.files
		v.invalidateFiles()
	}
	return nil
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

type diskWrite struct {
	path  string
	entry diskCacheEntry
}

var (
	diskWrites     = make(chan diskWrite, 256)
	diskWriterOnce sync.Once
)

// saveDiskCache hands the entry to a single background writer, which keeps
// writes ordered and off the UI goroutine (patches can be megabytes).
func saveDiskCache(key string, e *prCacheEntry) {
	if demoMode {
		return
	}
	diskWriterOnce.Do(func() { go diskWriter() })
	w := diskWrite{diskCachePath(key), diskCacheEntry{Detail: e.detailJSON, DetailAt: e.detailAt, Patch: e.patch, Head: e.head}}
	select {
	case diskWrites <- w:
	default: // writer backed up; the next fetch will save again
	}
}

func diskWriter() {
	for w := range diskWrites {
		data, err := json.Marshal(w.entry)
		if err != nil {
			continue
		}
		os.MkdirAll(filepath.Dir(w.path), 0755)
		tmp := w.path + ".tmp"
		if os.WriteFile(tmp, data, 0644) == nil {
			os.Rename(tmp, w.path)
		}
	}
}

type diskCacheLoadedMsg struct {
	key      string
	detail   *prDetail
	raw      []byte
	fp       string
	detailAt time.Time
	files    []*diffFile // nil unless the patch was parsed
	patch    string
	head     string
}

// loadDiskCacheCmd reads a cached PR for display, parsing its patch.
func loadDiskCacheCmd(pr PR) tea.Cmd {
	return func() tea.Msg {
		if msg, ok := readDiskCache(pr, true); ok {
			return msg
		}
		return nil
	}
}

// readDiskCache loads a cached PR off the UI goroutine. Parsing and
// highlighting the patch is the costly part, so warming skips it.
func readDiskCache(pr PR, parse bool) (diskCacheLoadedMsg, bool) {
	key := prKey(pr)
	data, err := os.ReadFile(diskCachePath(key))
	if err != nil || demoMode {
		return diskCacheLoadedMsg{}, false
	}
	var de diskCacheEntry
	if json.Unmarshal(stripJSONControls(data), &de) != nil {
		return diskCacheLoadedMsg{}, false
	}
	msg := diskCacheLoadedMsg{key: key, raw: de.Detail, fp: detailFingerprint(de.Detail), detailAt: de.DetailAt, patch: de.Patch, head: de.Head}
	if len(de.Detail) > 0 {
		var d prDetail
		if json.Unmarshal(de.Detail, &d) == nil {
			msg.detail = &d
		}
	}
	if parse && de.Patch != "" {
		msg.files = parseUnifiedDiff(de.Patch)
		highlightFiles(msg.files)
	}
	return msg, true
}

// handleDiskCacheLoaded fills gaps only; network results always win.
func (m *model) handleDiskCacheLoaded(msg diskCacheLoadedMsg, wantImages bool) tea.Cmd {
	e := m.cacheEntry(msg.key)
	if e.detail == nil && msg.detail != nil {
		e.detail, e.detailJSON, e.detailFP, e.detailAt = msg.detail, msg.raw, msg.fp, msg.detailAt
	}
	if !e.hasPatch && msg.head != "" {
		e.patch, e.head, e.hasPatch, e.files = msg.patch, msg.head, true, msg.files
	}
	var cmds []tea.Cmd
	if wantImages {
		// Cached signed URLs have likely expired, but the image disk cache may have them.
		cmds = append(cmds, queueImages(e.detail, false))
	}
	v := m.detail
	if v == nil || prKey(v.pr) != msg.key {
		return tea.Batch(cmds...)
	}
	if v.detail == nil && e.detail != nil {
		v.applyDetail(e.detail)
	}
	if v.diff == nil {
		if e.files != nil {
			v.diff = e.files
			v.diffLoading = e.patchPending || !e.patchFresh(v.pr)
			v.invalidateFiles()
		} else {
			cmds = append(cmds, m.ensureParsed(msg.key, e))
		}
	}
	v.clampScroll(m.height)
	return tea.Batch(cmds...)
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
