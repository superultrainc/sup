package main

import (
	"strconv"
	"strings"
	"unicode"
)

type diffLineKind int

const (
	lineContext diffLineKind = iota
	lineAdd
	lineDel
)

type span struct{ start, end int } // byte offsets into diffLine.text

type diffLine struct {
	kind         diffLineKind
	oldNo, newNo int // 0 when the line doesn't exist on that side
	text         string
	emph         []span // word-level changes vs the paired line
	segs         []seg  // syntax-highlighted runs covering text
}

type diffHunk struct {
	oldStart, oldCount int
	newStart, newCount int
	header             string // function context after the second @@
	lines              []diffLine
}

type diffFile struct {
	oldPath, newPath     string
	status               string // added, deleted, renamed, modified
	binary               bool
	hunks                []diffHunk
	additions, deletions int
}

func (f *diffFile) path() string {
	if f.newPath != "" {
		return f.newPath
	}
	return f.oldPath
}

func (f *diffFile) lineCount() int {
	n := 0
	for _, h := range f.hunks {
		n += len(h.lines)
	}
	return n
}

const tabWidth = 4

func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// parseUnifiedDiff parses `git diff` output. File-header lines are only
// recognised before a file's first hunk so `--- x` deletions aren't misread.
func parseUnifiedDiff(patch string) []*diffFile {
	patch = stripControls(patch) // file contents are untrusted; keep them from driving the terminal
	var files []*diffFile
	var f *diffFile
	var h *diffHunk
	oldNo, newNo := 0, 0
	oldLeft, newLeft := 0, 0 // lines still expected in the current hunk
	for _, l := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			f = &diffFile{status: "modified"}
			files = append(files, f)
			h = nil
			f.oldPath, f.newPath = splitGitPaths(strings.TrimPrefix(l, "diff --git "))
		case f == nil:
		case h == nil && strings.HasPrefix(l, "new file mode"):
			f.status = "added"
		case h == nil && strings.HasPrefix(l, "deleted file mode"):
			f.status = "deleted"
		case h == nil && strings.HasPrefix(l, "rename from "):
			f.oldPath, f.status = strings.TrimPrefix(l, "rename from "), "renamed"
		case h == nil && strings.HasPrefix(l, "rename to "):
			f.newPath, f.status = strings.TrimPrefix(l, "rename to "), "renamed"
		case h == nil && (strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch"):
			f.binary = true
		case h == nil && strings.HasPrefix(l, "--- "):
			f.oldPath = devNullPath(strings.TrimPrefix(l, "--- "), "a/")
		case h == nil && strings.HasPrefix(l, "+++ "):
			f.newPath = devNullPath(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "@@"):
			hk, ok := parseHunkHeader(l)
			if !ok {
				continue
			}
			f.hunks = append(f.hunks, hk)
			h = &f.hunks[len(f.hunks)-1]
			oldNo, newNo = hk.oldStart, hk.newStart
			oldLeft, newLeft = hk.oldCount, hk.newCount
		case h != nil && (l != "" || (oldLeft > 0 && newLeft > 0)):
			// Some tools strip the space from blank context lines.
			if l == "" {
				l = " "
			}
			text := expandTabs(l[1:])
			switch l[0] {
			case '+':
				h.lines = append(h.lines, diffLine{kind: lineAdd, newNo: newNo, text: text})
				newNo++
				newLeft--
				f.additions++
			case '-':
				h.lines = append(h.lines, diffLine{kind: lineDel, oldNo: oldNo, text: text})
				oldNo++
				oldLeft--
				f.deletions++
			case ' ':
				h.lines = append(h.lines, diffLine{kind: lineContext, oldNo: oldNo, newNo: newNo, text: text})
				oldNo++
				newNo++
				oldLeft--
				newLeft--
			}
		}
	}
	for _, f := range files {
		for i := range f.hunks {
			pairWordDiffs(f.hunks[i].lines)
		}
	}
	return files
}

// splitGitPaths splits "a/x b/x" from a diff --git line; rename lines refine it later.
func splitGitPaths(s string) (string, string) {
	if strings.HasPrefix(s, "a/") {
		for i := 2; i < len(s); i++ {
			if strings.HasPrefix(s[i:], " b/") && s[2:i] == s[i+3:] {
				return s[2:i], s[i+3:]
			}
		}
		if i := strings.Index(s, " b/"); i >= 0 {
			return s[2:i], s[i+3:]
		}
	}
	return s, s
}

// devNullPath strips the a/ or b/ prefix; /dev/null means the side doesn't exist.
func devNullPath(p, prefix string) string {
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, prefix)
}

func parseHunkHeader(l string) (diffHunk, bool) {
	// @@ -a[,b] +c[,d] @@ header
	end := strings.Index(l[2:], "@@")
	if end < 0 {
		return diffHunk{}, false
	}
	fields := strings.Fields(l[2 : end+2])
	if len(fields) < 2 {
		return diffHunk{}, false
	}
	var h diffHunk
	h.oldStart, h.oldCount = parseRange(strings.TrimPrefix(fields[0], "-"))
	h.newStart, h.newCount = parseRange(strings.TrimPrefix(fields[1], "+"))
	h.header = strings.TrimSpace(l[end+4:])
	return h, true
}

func parseRange(s string) (int, int) {
	start, count, found := strings.Cut(s, ",")
	a, _ := strconv.Atoi(start)
	b := 1
	if found {
		b, _ = strconv.Atoi(count)
	}
	return a, b
}

// pairWordDiffs pairs each run of deletions with the additions that follow it
// line by line, and marks the words that changed between each pair.
func pairWordDiffs(lines []diffLine) {
	for i := 0; i < len(lines); {
		if lines[i].kind != lineDel {
			i++
			continue
		}
		delStart := i
		for i < len(lines) && lines[i].kind == lineDel {
			i++
		}
		addStart := i
		for i < len(lines) && lines[i].kind == lineAdd {
			i++
		}
		for k := 0; delStart+k < addStart && addStart+k < i; k++ {
			a, b := &lines[delStart+k], &lines[addStart+k]
			a.emph, b.emph = wordDiff(a.text, b.text)
		}
	}
}

const (
	maxLineDiffLength = 1000
	maxWordTokens     = 400
)

type wordToken struct {
	text       string
	start, end int
}

func tokenizeWords(s string) []wordToken {
	var toks []wordToken
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	start, prev := 0, -1
	for i, r := range s {
		c := class(r)
		// Punctuation is always its own token; words and whitespace run together.
		if i > start && (c != prev || c == 0) {
			toks = append(toks, wordToken{s[start:i], start, i})
			start = i
		}
		prev = c
	}
	if start < len(s) {
		toks = append(toks, wordToken{s[start:], start, len(s)})
	}
	return toks
}

// wordDiff returns the changed spans in a and b. It returns nil spans when the
// lines are too long or mostly different, since the line tint already says that.
func wordDiff(a, b string) ([]span, []span) {
	if len(a) > maxLineDiffLength || len(b) > maxLineDiffLength {
		return nil, nil
	}
	ta, tb := tokenizeWords(a), tokenizeWords(b)
	if len(ta) > maxWordTokens || len(tb) > maxWordTokens {
		return nil, nil
	}
	n, m := len(ta), len(tb)
	keepA, keepB := make([]bool, n), make([]bool, m)
	common := 0
	keep := func(i, j int) {
		keepA[i], keepB[j] = true, true
		if strings.TrimSpace(ta[i].text) != "" {
			common += len(ta[i].text)
		}
	}
	// Shared prefix and suffix first; LCS alone tends to scatter matches
	// (e.g. pairing ", " inside a new call with the old separator).
	pre := 0
	for pre < n && pre < m && ta[pre].text == tb[pre].text {
		keep(pre, pre)
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && ta[n-1-suf].text == tb[m-1-suf].text {
		keep(n-1-suf, m-1-suf)
		suf++
	}

	// Prefix LCS on the middle, backtracked from the end so repeated tokens
	// match their last occurrence and changes stay contiguous.
	ma, mb := ta[pre:n-suf], tb[pre:m-suf]
	dp := make([][]int, len(ma)+1)
	for i := range dp {
		dp[i] = make([]int, len(mb)+1)
	}
	for i := 1; i <= len(ma); i++ {
		for j := 1; j <= len(mb); j++ {
			if ma[i-1].text == mb[j-1].text {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	for i, j := len(ma), len(mb); i > 0 && j > 0; {
		switch {
		case ma[i-1].text == mb[j-1].text:
			keep(pre+i-1, pre+j-1)
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			i--
		default:
			j--
		}
	}
	nonSpace := func(s string) int { return len(strings.Join(strings.Fields(s), "")) }
	shorter := min(nonSpace(a), nonSpace(b))
	if shorter == 0 || common*10 < shorter*4 {
		return nil, nil
	}
	return changedSpans(ta, keepA), changedSpans(tb, keepB)
}

// changedSpans merges changed tokens, bridging whitespace-only gaps so a
// changed phrase highlights as one block.
func changedSpans(toks []wordToken, keep []bool) []span {
	var out []span
	for i := 0; i < len(toks); i++ {
		if keep[i] {
			continue
		}
		s := span{toks[i].start, toks[i].end}
		for i+1 < len(toks) {
			switch {
			case !keep[i+1]:
				i++
				s.end = toks[i].end
				continue
			case i+2 < len(toks) && !keep[i+2] && strings.TrimSpace(toks[i+1].text) == "":
				i += 2
				s.end = toks[i].end
				continue
			}
			break
		}
		out = append(out, s)
	}
	return out
}
