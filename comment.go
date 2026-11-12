package main

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// commentDialog is the in-app editor for a review comment or change request.
type commentDialog struct {
	action  string // "comment" or "request-changes"
	pr      PR
	buf     []rune
	pos     int // cursor, as an index into buf
	people  []reviewerCandidate
	matches []reviewerCandidate // @-completions for the word at the cursor
	pick    int
}

const commentMaxLines = 12

// openComment shows the comment modal, restoring an unsent draft and loading people for @-completion.
func (m *model) openComment(action string, pr PR) tea.Cmd {
	key := prKey(pr)
	cd := &commentDialog{action: action, pr: pr, buf: []rune(m.drafts[action+" "+key])}
	cd.pos = len(cd.buf)
	m.comment = cd
	m.actionStatus = ""
	if people := loadPeople(pr.Repository.Owner.Login); len(people) > 0 {
		cd.setPeople(people, m.commentDetail())
		return nil
	}
	return fetchReviewerCandidatesCmd(pr)
}

func (m model) commentDetail() *prDetail {
	if e := m.cache[prKey(m.comment.pr)]; e != nil {
		return e.detail
	}
	return nil
}

// setPeople puts the PR's own participants first, then everyone else in the org.
func (cd *commentDialog) setPeople(cands []reviewerCandidate, d *prDetail) {
	seen := map[string]bool{currentUser: true}
	var out []reviewerCandidate
	add := func(c reviewerCandidate) {
		if c.id != "" && !seen[c.id] {
			seen[c.id] = true
			out = append(out, c)
		}
	}
	add(reviewerCandidate{id: cd.pr.Author.Login})
	if d != nil {
		for _, c := range d.Comments.Nodes {
			add(reviewerCandidate{id: c.Author.Login})
		}
		for _, r := range d.Reviews.Nodes {
			add(reviewerCandidate{id: r.Author.Login})
		}
		for _, t := range d.ReviewThreads.Nodes {
			for _, c := range t.Comments.Nodes {
				add(reviewerCandidate{id: c.Author.Login})
			}
		}
	}
	for id := range requestedIDs(cd.pr, d) {
		add(reviewerCandidate{id: id, team: strings.Contains(id, "/")})
	}
	// Participants have no display name yet; borrow it from the org list.
	names := map[string]string{}
	for _, c := range cands {
		names[c.id] = c.name
	}
	for i := range out {
		out[i].name = names[out[i].id]
	}
	for _, c := range cands {
		add(c)
	}
	cd.people = out
	cd.complete()
}

// mentionQuery returns the partial login after an @ just before the cursor.
func (cd *commentDialog) mentionQuery() (q string, start int, ok bool) {
	i := cd.pos
	for i > 0 && isLoginRune(cd.buf[i-1]) {
		i--
	}
	if i == 0 || cd.buf[i-1] != '@' {
		return "", 0, false
	}
	// "a@b" is an email, not a mention.
	if i >= 2 && !unicode.IsSpace(cd.buf[i-2]) && !strings.ContainsRune("([{\"'", cd.buf[i-2]) {
		return "", 0, false
	}
	return string(cd.buf[i:cd.pos]), i, true
}

func isLoginRune(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '/' || r == '_')
}

// complete refreshes matches: prefix hits on login first, then substring hits on login or name.
func (cd *commentDialog) complete() {
	cd.matches, cd.pick = nil, 0
	q, _, ok := cd.mentionQuery()
	if !ok {
		return
	}
	q = strings.ToLower(q)
	var prefix, rest []reviewerCandidate
	for _, c := range cd.people {
		id := strings.ToLower(c.id)
		switch {
		case strings.HasPrefix(id, q):
			prefix = append(prefix, c)
		case strings.Contains(id, q) || strings.Contains(strings.ToLower(c.name), q):
			rest = append(rest, c)
		}
	}
	cd.matches = append(prefix, rest...)
	if len(cd.matches) > 5 {
		cd.matches = cd.matches[:5]
	}
}

// accept replaces the partial mention with the picked login and a space.
func (cd *commentDialog) accept() {
	_, start, ok := cd.mentionQuery()
	if !ok || len(cd.matches) == 0 {
		return
	}
	ins := []rune(cd.matches[cd.pick].id + " ")
	cd.buf = append(cd.buf[:start], append(ins, cd.buf[cd.pos:]...)...)
	cd.pos = start + len(ins)
	cd.matches = nil
}

func (cd *commentDialog) insert(rs []rune) {
	cd.buf = append(cd.buf[:cd.pos], append(append([]rune{}, rs...), cd.buf[cd.pos:]...)...)
	cd.pos += len(rs)
}

// lineStart and lineEnd bound the logical line around cursor position p.
func (cd *commentDialog) lineStart(p int) int {
	for p > 0 && cd.buf[p-1] != '\n' {
		p--
	}
	return p
}

func (cd *commentDialog) lineEnd(p int) int {
	for p < len(cd.buf) && cd.buf[p] != '\n' {
		p++
	}
	return p
}

// moveLine moves the cursor up (-1) or down (1) a logical line, keeping its column.
func (cd *commentDialog) moveLine(dir int) {
	start := cd.lineStart(cd.pos)
	col := cd.pos - start
	if dir < 0 {
		if start == 0 {
			cd.pos = 0
			return
		}
		prev := cd.lineStart(start - 1)
		cd.pos = min(prev+col, start-1)
		return
	}
	end := cd.lineEnd(cd.pos)
	if end == len(cd.buf) {
		cd.pos = end
		return
	}
	cd.pos = min(end+1+col, cd.lineEnd(end+1))
}

func (m model) handleCommentInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cd := m.comment
	draftKey := cd.action + " " + prKey(cd.pr)
	if len(cd.matches) > 0 {
		switch msg.String() {
		case "tab", "enter":
			cd.accept()
			return m, nil
		case "up", "ctrl+p":
			cd.pick = max(cd.pick-1, 0)
			return m, nil
		case "down", "ctrl+n":
			cd.pick = min(cd.pick+1, len(cd.matches)-1)
			return m, nil
		case "esc":
			cd.matches = nil
			return m, nil
		}
	}
	switch msg.String() {
	case "esc":
		// Keep the text so reopening picks up where it left off.
		if m.drafts == nil {
			m.drafts = map[string]string{}
		}
		m.drafts[draftKey] = string(cd.buf)
		m.comment = nil
		return m, nil
	case "ctrl+s", "alt+enter":
		body := strings.TrimSpace(string(cd.buf))
		if body == "" {
			return m, nil
		}
		delete(m.drafts, draftKey)
		m.comment = nil
		m.actionStatus = reviewDoneText(cd.action, cd.pr)
		return m, submitReviewCmd(cd.action, cd.pr, body)
	case "enter", "ctrl+j":
		cd.insert([]rune{'\n'})
	case "backspace", "ctrl+h":
		if cd.pos > 0 {
			cd.buf = append(cd.buf[:cd.pos-1], cd.buf[cd.pos:]...)
			cd.pos--
		}
	case "delete", "ctrl+d":
		if cd.pos < len(cd.buf) {
			cd.buf = append(cd.buf[:cd.pos], cd.buf[cd.pos+1:]...)
		}
	case "alt+backspace", "ctrl+w":
		p := cd.pos
		for p > 0 && unicode.IsSpace(cd.buf[p-1]) {
			p--
		}
		for p > 0 && !unicode.IsSpace(cd.buf[p-1]) {
			p--
		}
		cd.buf = append(cd.buf[:p], cd.buf[cd.pos:]...)
		cd.pos = p
	case "ctrl+u": // cmd+backspace in most macOS terminals
		p := cd.lineStart(cd.pos)
		cd.buf = append(cd.buf[:p], cd.buf[cd.pos:]...)
		cd.pos = p
	case "left", "ctrl+b":
		cd.pos = max(cd.pos-1, 0)
	case "right", "ctrl+f":
		cd.pos = min(cd.pos+1, len(cd.buf))
	case "alt+left", "alt+b":
		for cd.pos > 0 && unicode.IsSpace(cd.buf[cd.pos-1]) {
			cd.pos--
		}
		for cd.pos > 0 && !unicode.IsSpace(cd.buf[cd.pos-1]) {
			cd.pos--
		}
	case "alt+right", "alt+f":
		for cd.pos < len(cd.buf) && unicode.IsSpace(cd.buf[cd.pos]) {
			cd.pos++
		}
		for cd.pos < len(cd.buf) && !unicode.IsSpace(cd.buf[cd.pos]) {
			cd.pos++
		}
	case "home", "ctrl+a":
		cd.pos = cd.lineStart(cd.pos)
	case "end", "ctrl+e":
		cd.pos = cd.lineEnd(cd.pos)
	case "up":
		cd.moveLine(-1)
	case "down":
		cd.moveLine(1)
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			cd.insert([]rune(strings.ReplaceAll(string(msg.Runes), "\r\n", "\n")))
		}
	}
	cd.complete()
	return m, nil
}

// commentLines hard-wraps the buffer to width with the cursor drawn as a block,
// returning the lines and which one holds the cursor.
// shortcut: wraps by rune count, so wide runes (CJK, emoji) can overflow a line; measure cell width if that bites.
func (cd *commentDialog) commentLines(width int, text, cursor painter) ([]string, int) {
	var out []string
	var cur strings.Builder
	col, cursorLine := 0, 0
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
		col = 0
	}
	for i := 0; i <= len(cd.buf); i++ {
		if i == len(cd.buf) || cd.buf[i] == '\n' {
			if i == cd.pos {
				cursorLine = len(out)
				cur.WriteString(cursor.Render(" "))
			}
			flush()
			continue
		}
		if col >= width {
			flush()
		}
		if i == cd.pos {
			cursorLine = len(out)
			cur.WriteString(cursor.Render(string(cd.buf[i])))
		} else {
			cur.WriteString(text.Render(string(cd.buf[i])))
		}
		col++
	}
	return out, cursorLine
}

// commentView is the centered comment modal.
func (m model) commentView() string {
	cd := m.comment
	th := currentDiffTheme()
	width := min(72, max(m.width-6, 30))
	muted := sty(th.muted, "", false, false)
	text := sty(th.text, "", false, false)
	color, title, label := th.threadBorder, fmt.Sprintf("Comment on PR #%d", cd.pr.Number), "Comment"
	if cd.action == "request-changes" {
		color, title, label = th.delBar, fmt.Sprintf("Request changes on PR #%d", cd.pr.Number), "Request changes"
	}

	body := []string{
		sty(color, "", true, false).Render(title),
		muted.Render(truncate(cd.pr.Title, width)),
		"",
	}
	lines, at := cd.commentLines(width, text, painter{"\x1b[7m", "\x1b[27m"})
	if len(cd.buf) == 0 {
		lines[0] += muted.Render("Write a comment, @ to mention")
	}
	top := max(0, min(at-commentMaxLines+1, len(lines)-commentMaxLines))
	lines = lines[top:min(top+commentMaxLines, len(lines))]
	for len(lines) < 4 {
		lines = append(lines, "")
	}
	body = append(body, lines...)

	if len(cd.matches) > 0 {
		body = append(body, "")
		for i, c := range cd.matches {
			row := "@" + c.id
			if c.name != "" {
				row += "  " + muted.Render(c.name)
			}
			if i == cd.pick {
				row = sty(color, "", true, false).Render("› ") + sty(th.text, "", true, false).Render("@"+c.id) + strings.TrimPrefix(row, "@"+c.id)
			} else {
				row = "  " + text.Render("@"+c.id) + strings.TrimPrefix(row, "@"+c.id)
			}
			body = append(body, truncateToWidth(row, width))
		}
	}

	hint := muted.Render("enter newline")
	if len(cd.matches) > 0 {
		hint = muted.Render("tab/enter pick · ↑↓ choose")
	}
	body = append(body, "",
		sty("16", color, true, false).Render(" ctrl+s ")+" "+text.Render(label)+"    "+
			sty(th.text, th.chipBg, false, false).Render(" esc ")+" "+muted.Render("Close")+"    "+hint)
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(color)).Padding(1, 3).Width(width + 6)
	return frame.Render(strings.Join(body, "\n"))
}
