package main

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// seg is a run of text sharing one syntax style.
type seg struct {
	text         string
	fg           string
	bold, italic bool
}

type diffTheme struct {
	chroma                         string
	addBg, addEmph, addBar, addNum string
	delBg, delEmph, delBar, delNum string
	sepBg, sepFg, numFg, emptyBg   string
	divider, border, threadBorder  string
	text, muted, band, authorBand  string // band/authorBand fill card header rows; empty means no fill
	chipBg, branchFg, branchBg     string
	selBg                          string // selected row in the file tree
	accent                         string // active tab underline
}

// Colors follow GitHub's diff palette, pre-blended onto its page background.
var darkDiffTheme = diffTheme{
	chroma: "github-dark",
	addBg:  "#12261e", addEmph: "#1f5a32", addBar: "#3fb950", addNum: "#7ee2a8",
	delBg: "#2a1418", delEmph: "#6e2229", delBar: "#f85149", delNum: "#ffa198",
	sepBg: "#161b22", sepFg: "#8b949e", numFg: "#6e7681", emptyBg: "#14181f",
	divider: "#30363d", border: "#3d444d", threadBorder: "#d29922",
	text: "#e6edf3", muted: "#8b949e",
	chipBg: "#30363d", branchFg: "#58a6ff", branchBg: "#121d2f", accent: "#f78166", selBg: "#161b22",
}

var lightDiffTheme = diffTheme{
	chroma: "github",
	addBg:  "#e6ffec", addEmph: "#abf2bc", addBar: "#1a7f37", addNum: "#1a7f37",
	delBg: "#ffebe9", delEmph: "#ffc1bf", delBar: "#cf222e", delNum: "#cf222e",
	sepBg: "#f6f8fa", sepFg: "#57606a", numFg: "#8c959f", emptyBg: "#f6f8fa",
	divider: "#d0d7de", border: "#d0d7de", threadBorder: "#bf8700",
	text: "#1f2328", muted: "#59636e",
	chipBg: "#d1d9e0", branchFg: "#0969da", branchBg: "#ddf4ff", accent: "#fd8c73", selBg: "#f6f8fa",
}

func currentDiffTheme() diffTheme {
	if glamourStyle == "light" {
		return lightDiffTheme
	}
	return darkDiffTheme
}

const maxHighlightLines = 5000

// highlightFile tokenizes each side of the file as one document so lexer
// state carries across lines. Context lines take their styling from the new side.
func highlightFile(f *diffFile, style *chroma.Style) {
	lexer := lexers.Match(filepath.Base(f.path()))
	if lexer == nil || f.lineCount() > maxHighlightLines {
		return
	}
	lexer = chroma.Coalesce(lexer)
	var oldRefs, newRefs []*diffLine
	for hi := range f.hunks {
		for li := range f.hunks[hi].lines {
			l := &f.hunks[hi].lines[li]
			if l.kind != lineAdd {
				oldRefs = append(oldRefs, l)
			}
			if l.kind != lineDel {
				newRefs = append(newRefs, l)
			}
		}
	}
	tokenize := func(refs []*diffLine, assign func(*diffLine) bool) {
		texts := make([]string, len(refs))
		for i, l := range refs {
			texts[i] = l.text
		}
		it, err := lexer.Tokenise(nil, strings.Join(texts, "\n")+"\n")
		if err != nil {
			return
		}
		idx := 0
		var cur []seg
		flush := func() {
			if idx < len(refs) && assign(refs[idx]) {
				var b strings.Builder
				for _, s := range cur {
					b.WriteString(s.text)
				}
				if b.String() == refs[idx].text {
					refs[idx].segs = cur
				}
			}
			idx++
			cur = nil
		}
		for tok := it(); tok != chroma.EOF; tok = it() {
			e := style.Get(tok.Type)
			fg := ""
			if e.Colour.IsSet() {
				fg = e.Colour.String()
			}
			for i, part := range strings.Split(tok.Value, "\n") {
				if i > 0 {
					flush()
				}
				if part != "" {
					cur = append(cur, seg{part, fg, e.Bold == chroma.Yes, e.Italic == chroma.Yes})
				}
			}
		}
		if len(cur) > 0 {
			flush()
		}
	}
	tokenize(oldRefs, func(l *diffLine) bool { return l.kind == lineDel })
	tokenize(newRefs, func(*diffLine) bool { return true })
}

// highlightFiles highlights files in parallel; each file only touches its own lines.
func highlightFiles(files []*diffFile) {
	style := styles.Get(currentDiffTheme().chroma)
	work := make(chan *diffFile)
	var wg sync.WaitGroup
	for i := 0; i < min(runtime.GOMAXPROCS(0), len(files)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				highlightFile(f, style)
			}
		}()
	}
	// Biggest first so one large file doesn't start last and tail the batch.
	order := append([]*diffFile(nil), files...)
	sort.Slice(order, func(i, j int) bool { return order[i].lineCount() > order[j].lineCount() })
	for _, f := range order {
		if !f.binary {
			work <- f
		}
	}
	close(work)
	wg.Wait()
}

// renderCode lays out a line's highlighted text into rows exactly width cells
// wide, wrapping or truncating (with ›) as asked.
func renderCode(l *diffLine, width int, wrap bool, bg, emphBg string) []string {
	segs := l.segs
	if segs == nil {
		segs = []seg{{text: l.text}}
	}
	if width < 2 {
		width = 2
	}
	truncated := !wrap && runewidth.StringWidth(l.text) > width
	limit := width
	if truncated {
		limit = width - 1
	}

	var rows []string
	var row, run strings.Builder
	rowW := 0
	var runStyle [3]any // fg, emph, bold/italic — compared to decide when to flush
	var runSeg seg
	runEmph := false
	flushRun := func() {
		if run.Len() == 0 {
			return
		}
		b := bg
		if runEmph {
			b = emphBg
		}
		row.WriteString(sty(runSeg.fg, b, runSeg.bold, runSeg.italic).Render(run.String()))
		run.Reset()
	}
	endRow := func() {
		flushRun()
		if rowW < width {
			row.WriteString(sty("", bg, false, false).Render(strings.Repeat(" ", width-rowW)))
		}
		rows = append(rows, row.String())
		row.Reset()
		rowW = 0
	}

	off := 0
	emph := l.emph
	stopped := false
	for _, s := range segs {
		for _, r := range s.text {
			inEmph := false
			for _, sp := range emph {
				if off >= sp.start && off < sp.end {
					inEmph = true
					break
				}
			}
			off += len(string(r))
			rw := runewidth.RuneWidth(r)
			if rowW+rw > limit {
				if !wrap {
					stopped = true
					break
				}
				endRow()
			}
			key := [3]any{s.fg, inEmph, [2]bool{s.bold, s.italic}}
			if key != runStyle {
				flushRun()
				runStyle, runSeg, runEmph = key, s, inEmph
			}
			run.WriteRune(r)
			rowW += rw
		}
		if stopped {
			break
		}
	}
	if truncated {
		flushRun()
		row.WriteString(sty(currentDiffTheme().sepFg, bg, false, false).Render("›"))
		rowW++
	}
	endRow()
	return rows
}

// diffRenderer turns parsed files into card-framed lines for the Files tab.
type diffRenderer struct {
	th          diffTheme
	width       int // full card width
	split       bool
	wrap        bool
	collapsed   map[string]bool
	noSummary   bool // the file tree replaces the summary card
	threads     []reviewThread
	lines       []string
	fileMarks   []int
	threadMarks []int
}

func (r *diffRenderer) inner() int { return r.width - 2 }

func (r *diffRenderer) emit(s string) { r.lines = append(r.lines, " "+s) }

func (r *diffRenderer) row(content string) { r.emit(cardRow(content, r.th.border, r.width)) }

func numWidth(f *diffFile) int {
	m := 0
	for _, h := range f.hunks {
		m = max(m, h.oldStart+h.oldCount, h.newStart+h.newCount)
	}
	return max(len(fmt.Sprint(m)), 3)
}

func isGeneratedPath(p string) bool {
	base := filepath.Base(p)
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "go.sum", "Cargo.lock", "Gemfile.lock",
		"poetry.lock", "composer.lock", "Package.resolved", "bun.lockb", "uv.lock":
		return true
	}
	return strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") ||
		strings.HasSuffix(base, ".pbxproj") || strings.HasSuffix(base, ".snap")
}

func (r *diffRenderer) isCollapsed(f *diffFile) bool {
	if c, ok := r.collapsed[f.path()]; ok {
		return c
	}
	return isGeneratedPath(f.path()) || f.lineCount() > 1500
}

func statusLetter(f *diffFile) string {
	switch f.status {
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "renamed":
		return "R"
	}
	return "M"
}

func statusColor(f *diffFile, th diffTheme) string {
	switch f.status {
	case "added":
		return th.addBar
	case "deleted":
		return th.delBar
	case "renamed":
		return th.branchFg
	}
	return th.threadBorder
}

func fileStatusBadge(f *diffFile) string {
	return sty(statusColor(f, currentDiffTheme()), "", true, false).Render(statusLetter(f))
}

func (r *diffRenderer) threadsFor(f *diffFile) []reviewThread {
	var out []reviewThread
	for _, t := range r.threads {
		if t.Path == f.path() || (f.oldPath != "" && t.Path == f.oldPath) {
			out = append(out, t)
		}
	}
	return out
}

func (r *diffRenderer) summary(files []*diffFile) {
	adds, dels := 0, 0
	addW, delW := 0, 0
	for _, f := range files {
		adds += f.additions
		dels += f.deletions
		addW = max(addW, len(fmt.Sprintf("+%d", f.additions)))
		delW = max(delW, len(fmt.Sprintf("-%d", f.deletions)))
	}
	band := r.th.band
	left := sty(r.th.text, band, true, false).Render("Files changed") + sty(r.th.muted, band, false, false).Render(fmt.Sprintf(" %d", len(files)))
	right := sty(r.th.addBar, band, false, false).Render(fmt.Sprintf("+%d", adds)) + sty(r.th.delBar, band, false, false).Render(fmt.Sprintf(" −%d", dels))
	var body []string
	for _, f := range files {
		line := fileStatusBadge(f) + "  " +
			additionsStyle.Render(padLeft(fmt.Sprintf("+%d", f.additions), addW)) + " " +
			deletionsStyle.Render(padLeft(fmt.Sprintf("-%d", f.deletions), delW)) + "  " + pathStyle.Render(f.path())
		if n := len(r.threadsFor(f)); n > 0 {
			line += reviewRequestedStyle.Render(fmt.Sprintf("  ◆ %d", n))
		}
		body = append(body, line)
	}
	for _, l := range bandCard(left, right, body, r.width, r.th.border, band) {
		r.emit(l)
	}
}

func (r *diffRenderer) render(files []*diffFile) {
	if len(files) == 0 {
		r.emit(metaStyle.Render("No file changes."))
		return
	}
	if !r.noSummary {
		r.summary(files)
	}
	for i, f := range files {
		if i > 0 || !r.noSummary {
			r.emit("")
		}
		r.fileMarks = append(r.fileMarks, len(r.lines))
		r.file(f)
	}
}

func (r *diffRenderer) file(f *diffFile) {
	name := pathStyle.Render(f.path())
	if f.status == "renamed" && f.oldPath != f.newPath {
		name = metaStyle.Render(f.oldPath+" → ") + pathStyle.Render(f.newPath)
	}
	threads := r.threadsFor(f)
	title := fileStatusBadge(f) + " " + name + "  " +
		additionsStyle.Render(fmt.Sprintf("+%d", f.additions)) + " " + deletionsStyle.Render(fmt.Sprintf("-%d", f.deletions))
	if len(threads) > 0 {
		title += reviewRequestedStyle.Render(fmt.Sprintf("  ◆ %d", len(threads)))
	}
	r.emit(cardTop(title, r.th.border, r.width))
	defer func() { r.emit(cardBottom(r.th.border, r.width)) }()

	switch {
	case f.binary:
		r.row(metaStyle.Render("  Binary file not shown"))
		return
	case len(f.hunks) == 0:
		r.row(metaStyle.Render("  No content changes"))
		return
	case r.isCollapsed(f):
		why := "large diff"
		if isGeneratedPath(f.path()) {
			why = "generated file"
		}
		r.row(metaStyle.Render(fmt.Sprintf("  ▸ Collapsed: %s · %d lines · x to expand", why, f.lineCount())))
		return
	}

	type anchor struct {
		left bool
		line int
	}
	byLine := map[anchor][]reviewThread{}
	var outdated []reviewThread
	for _, t := range threads {
		if t.Line == nil {
			outdated = append(outdated, t)
			continue
		}
		a := anchor{t.DiffSide == "LEFT", *t.Line}
		byLine[a] = append(byLine[a], t)
	}
	placed := map[anchor]bool{}
	place := func(left bool, line int) {
		a := anchor{left, line}
		if line == 0 || placed[a] {
			return
		}
		for _, t := range byLine[a] {
			placed[a] = true
			r.thread(t, "")
		}
	}

	for _, t := range outdated {
		label := "outdated"
		if t.OriginalLine != nil {
			label = fmt.Sprintf("outdated · was line %d", *t.OriginalLine)
		}
		r.thread(t, label)
	}

	nw := numWidth(f)
	prevEnd := 1
	for hi := range f.hunks {
		h := &f.hunks[hi]
		r.separator(h.newStart-prevEnd, h.header)
		prevEnd = h.newStart + h.newCount
		// A one-sided file would leave half the split empty.
		if r.split && f.status != "added" && f.status != "deleted" {
			r.splitHunk(h, nw, place)
		} else {
			for li := range h.lines {
				l := &h.lines[li]
				r.unifiedLine(l, nw)
				if l.kind != lineDel {
					place(false, l.newNo)
				}
				if l.kind != lineAdd {
					place(true, l.oldNo)
				}
			}
		}
	}
	// Anything whose line wasn't in the hunks still gets shown.
	for a, ts := range byLine {
		if !placed[a] {
			for _, t := range ts {
				r.thread(t, fmt.Sprintf("line %d", a.line))
			}
		}
	}
}

func (r *diffRenderer) separator(gap int, header string) {
	if gap <= 0 && header == "" {
		return
	}
	text := "  "
	if gap > 0 {
		noun := "lines"
		if gap == 1 {
			noun = "line"
		}
		text += fmt.Sprintf("⋯ %d unchanged %s", gap, noun)
	} else {
		text += "⋯"
	}
	if header != "" {
		text += "   " + header
	}
	r.row(sty(r.th.sepFg, r.th.sepBg, false, false).Render(fitWidth(text, r.inner())))
}

func (r *diffRenderer) lineColors(l *diffLine) (bg, emph, bar, num string) {
	switch l.kind {
	case lineAdd:
		return r.th.addBg, r.th.addEmph, r.th.addBar, r.th.addNum
	case lineDel:
		return r.th.delBg, r.th.delEmph, r.th.delBar, r.th.delNum
	}
	return "", "", "", r.th.numFg
}

func lineNo(n int, w int) string {
	if n == 0 {
		return strings.Repeat(" ", w)
	}
	return padLeft(fmt.Sprint(n), w)
}

func (r *diffRenderer) unifiedLine(l *diffLine, nw int) {
	for _, row := range r.unifiedRows(l, nw) {
		r.row(row)
	}
}

// unifiedRows renders one line as rows exactly r.inner() wide.
func (r *diffRenderer) unifiedRows(l *diffLine, nw int) []string {
	bg, emph, bar, num := r.lineColors(l)
	gutterW := 2*nw + 5
	code := renderCode(l, r.inner()-gutterW, r.wrap, bg, emph)
	barStr := sty(bar, bg, false, false).Render("▌")
	if bar == "" {
		barStr = " "
	}
	rows := make([]string, len(code))
	for i, c := range code {
		nums := lineNo(l.oldNo, nw) + " " + lineNo(l.newNo, nw)
		if i > 0 {
			nums = strings.Repeat(" ", 2*nw+1)
		}
		rows[i] = barStr + sty(num, bg, false, false).Render(" "+nums+"  ") + c
	}
	return rows
}

// splitSide renders one half of a split row; nil means no line on that side.
func (r *diffRenderer) splitSide(l *diffLine, left bool, width, nw int) []string {
	if l == nil {
		return []string{sty("", r.th.emptyBg, false, false).Render(strings.Repeat(" ", width))}
	}
	bg, emph, bar, num := r.lineColors(l)
	n := l.newNo
	if left {
		n = l.oldNo
	}
	barStr := sty(bar, bg, false, false).Render("▌")
	if bar == "" {
		barStr = " "
	}
	code := renderCode(l, width-nw-4, r.wrap, bg, emph)
	out := make([]string, len(code))
	for i, c := range code {
		no := lineNo(n, nw)
		if i > 0 {
			no = strings.Repeat(" ", nw)
		}
		out[i] = barStr + sty(num, bg, false, false).Render(" "+no+"  ") + c
	}
	return out
}

func (r *diffRenderer) splitHunk(h *diffHunk, nw int, place func(bool, int)) {
	leftW := (r.inner() - 1) / 2
	rightW := r.inner() - 1 - leftW
	div := sty(r.th.divider, "", false, false).Render("│")
	emitPair := func(a, b *diffLine) {
		ls, rs := r.splitSide(a, true, leftW, nw), r.splitSide(b, false, rightW, nw)
		blankL := sty("", lineBg(r, a), false, false).Render(strings.Repeat(" ", leftW))
		blankR := sty("", lineBg(r, b), false, false).Render(strings.Repeat(" ", rightW))
		for i := 0; i < max(len(ls), len(rs)); i++ {
			lc, rc := blankL, blankR
			if i < len(ls) {
				lc = ls[i]
			}
			if i < len(rs) {
				rc = rs[i]
			}
			r.row(lc + div + rc)
		}
		if a != nil {
			place(true, a.oldNo)
		}
		if b != nil {
			place(false, b.newNo)
		}
	}
	lines := h.lines
	for i := 0; i < len(lines); {
		if lines[i].kind == lineContext {
			emitPair(&lines[i], &lines[i])
			i++
			continue
		}
		var dels, adds []*diffLine
		for i < len(lines) && lines[i].kind == lineDel {
			dels = append(dels, &lines[i])
			i++
		}
		for i < len(lines) && lines[i].kind == lineAdd {
			adds = append(adds, &lines[i])
			i++
		}
		for k := 0; k < max(len(dels), len(adds)); k++ {
			var a, b *diffLine
			if k < len(dels) {
				a = dels[k]
			}
			if k < len(adds) {
				b = adds[k]
			}
			emitPair(a, b)
		}
	}
}

func lineBg(r *diffRenderer, l *diffLine) string {
	if l == nil {
		return r.th.emptyBg
	}
	bg, _, _, _ := r.lineColors(l)
	return bg
}

// thread draws a review thread as a nested card inside the file card.
func (r *diffRenderer) thread(t reviewThread, label string) {
	if len(t.Comments.Nodes) == 0 {
		return
	}
	w := r.inner() - 4
	r.threadMarks = append(r.threadMarks, len(r.lines))
	r.row("")
	for _, l := range threadCard(t, label, nil, w, r.th) {
		r.row("  " + l)
	}
	r.row("")
}

// threadCard draws a review thread with GitHub's shaded header.
func threadCard(t reviewThread, label string, snippet []string, width int, th diffTheme) []string {
	border := th.threadBorder
	if t.IsResolved {
		border = th.border
	}
	first := t.Comments.Nodes[0]
	left := sty(th.text, th.band, true, false).Render(first.Author.Login) +
		sty(th.muted, th.band, false, false).Render(" commented "+relTime(first.CreatedAt))
	if label != "" {
		left += sty(th.muted, th.band, false, false).Render(" · " + label)
	}
	right := ""
	if t.IsResolved {
		right = sty(th.addBar, th.band, false, false).Render("✓ Resolved")
	}
	body := snippet
	if len(snippet) > 0 {
		body = append(body, "")
	}
	for i, c := range t.Comments.Nodes {
		if i > 0 {
			body = append(body, sty(th.divider, "", false, false).Render(strings.Repeat("┄", width-4)))
			body = append(body, sty(th.text, "", true, false).Render(c.Author.Login)+sty(th.muted, "", false, false).Render(" "+relTime(c.CreatedAt)))
		}
		md := richBody(c.Body, width-4)
		if len(md) == 0 {
			md = []string{sty(th.muted, "", false, true).Render("(empty)")}
		}
		body = append(body, md...)
	}
	return bandCard(left, right, body, width, border, th.band)
}

type prPatchFetchedMsg struct {
	key   string
	files []*diffFile
	patch string
	head  string
	err   error
}

// fetchPatchCmd fetches, parses, and highlights off the UI goroutine.
// parse is false for background warming, which keeps only the raw patch.
func fetchPatchCmd(pr PR, parse bool) tea.Cmd {
	return func() tea.Msg {
		patch, err := fetchPRPatch(pr)
		if err != nil {
			return prPatchFetchedMsg{key: prKey(pr), err: err}
		}
		msg := prPatchFetchedMsg{key: prKey(pr), patch: string(patch), head: pr.HeadRefOid}
		if parse {
			msg.files = parseUnifiedDiff(msg.patch)
			highlightFiles(msg.files)
		}
		return msg
	}
}

func fetchPRPatch(pr PR) ([]byte, error) {
	if demoMode {
		return []byte(mockPatch), nil
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", githubAPI, pr.Repository.Owner.Login, pr.Repository.Name, pr.Number)
	get := func() (*http.Response, error) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+ghToken)
		req.Header.Set("Accept", "application/vnd.github.diff")
		return httpClient.Do(req)
	}
	resp, err := get()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		invalidateGHToken()
		if err := refreshGHToken(); err != nil {
			return nil, fmt.Errorf("auth refresh failed: %w", err)
		}
		if resp, err = get(); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == 406 {
			return nil, fmt.Errorf("diff too large for the GitHub API — press o to view it on GitHub")
		}
		return nil, fmt.Errorf("diff %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// fileTree is the Files tab's left column: files grouped under their
// directory, with the file at the top of the diff highlighted.
// fileAt maps each returned line to its file index (-1 for headers and
// directories) so clicks can be resolved.
func fileTree(files []*diffFile, current int, d *prDetail, width, height int) (lines []string, fileAt []int) {
	th := currentDiffTheme()
	muted := sty(th.muted, "", false, false)
	threads := map[string]int{}
	if d != nil {
		for _, t := range d.ReviewThreads.Nodes {
			threads[t.Path]++
		}
	}

	type row struct {
		text string
		file int
	}
	var rows []row
	prevDir := ""
	curRow := 0
	for i, f := range files {
		dir := filepath.Dir(f.path())
		if dir == "." {
			dir = ""
		}
		if dir != prevDir && dir != "" {
			rows = append(rows, row{muted.Render(truncate("▾ "+dir, width-1)), -1})
		}
		prevDir = dir
		indent := " "
		if dir != "" {
			indent = "   "
		}
		bg := ""
		if i == current {
			bg = th.selBg
			curRow = len(rows)
		}
		stats := sty(th.addBar, bg, false, false).Render(fmt.Sprintf("+%d", f.additions)) +
			sty(th.delBar, bg, false, false).Render(fmt.Sprintf(" −%d", f.deletions))
		if n := threads[f.path()]; n > 0 {
			stats = sty(th.threadBorder, bg, false, false).Render(fmt.Sprintf("◆%d ", n)) + stats
		}
		name := filepath.Base(f.path())
		nameW := width - len(indent) - 3 - lipgloss.Width(stats) - 2
		slot := sty("", bg, false, false).Render(" ")
		if i == current {
			slot = sty(th.accent, bg, true, false).Render("▎")
		}
		left := slot + sty(th.text, bg, false, false).Render(indent[1:]) + sty(statusColor(f, th), bg, true, false).Render(statusLetter(f)) +
			sty(th.text, bg, i == current, false).Render(" "+truncate(name, max(nameW, 4)))
		gap := width - lipgloss.Width(left) - lipgloss.Width(stats) - 1
		line := left + sty("", bg, false, false).Render(strings.Repeat(" ", max(gap, 1))) + stats + sty("", bg, false, false).Render(" ")
		rows = append(rows, row{ansi.Truncate(line, width, ""), i})
	}

	lines = []string{sty(th.muted, "", true, false).Render(" Files ") + chip(fmt.Sprint(len(files)), th.text, th.chipBg), ""}
	fileAt = []int{-1, -1}
	listH := max(height-len(lines), 1)
	start := 0
	if curRow >= listH {
		start = min(curRow-listH/2, len(rows)-listH)
	}
	for _, r := range rows[max(start, 0):min(max(start, 0)+listH, len(rows))] {
		lines = append(lines, r.text)
		fileAt = append(fileAt, r.file)
	}
	return lines, fileAt
}
