package main

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// previewActive is true when the list should split, with the cursor's PR on the right.
func (m model) previewActive() bool {
	return m.previewOn && m.detail == nil && m.width >= 100
}

func (m model) previewListWidth() int { return min(max(m.width*2/5, 40), 70) }

// syncPreview points the preview at the cursor's PR and picks up newly cached data.
func (m *model) syncPreview() {
	if !m.previewActive() {
		return
	}
	pr, ok := m.currentPR()
	if !ok {
		m.preview = nil
		return
	}
	w := max(m.width-m.previewListWidth()-1, 40)
	if m.preview == nil || prKey(m.preview.pr) != prKey(pr) {
		m.preview = newDetailView(pr)
	}
	v := m.preview
	v.pr = pr
	if e := m.cache[prKey(pr)]; e != nil && e.detail != nil && v.detail != e.detail {
		v.width = w
		v.applyDetail(e.detail)
	}
	if v.detail != nil && v.width != w {
		v.rebuild(w)
	}
	v.width = w
	v.loading = v.detail == nil
}

func (m model) splitView() string {
	listW := m.previewListWidth()
	lm := m
	lm.width, lm.compactList = listW, true
	left := strings.Split(lm.view(), "\n")

	var right []string
	if m.preview != nil {
		pm := m
		pm.detail, pm.width = m.preview, m.width-listW-1
		right = strings.Split(pm.detailView(), "\n")
		right = right[:max(len(right)-2, 0)] // the list's footer covers the bottom rows
	}

	divider := sty(currentDiffTheme().divider, "", false, false).Render("│")
	rows := make([]string, m.height)
	for i := range rows {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		rows[i] = fitWidth(l, listW) + divider + r
	}
	return strings.Join(rows, "\n")
}

// compactRow is a list row for the narrow preview list: status, number, title.
func compactRow(pr PR, selected bool, width int, lp listPainters) string {
	const colStatus, colNum = 12, 6
	kind := statusKind(pr)
	label := badgeLabels[kind]
	fill := spaces(max(colStatus-len(label), 0))
	num := pad("#"+strconv.Itoa(pr.Number), colNum)
	title := truncate(pr.Title, max(width-2-colStatus-colNum, 4))
	if selected {
		return lp.caret.Render("» ") + lp.selBadge[kind].Render(label) + fill + lp.selNormal.Render(num+title)
	}
	return "  " + lp.badge[kind].Render(label) + fill + lp.dim.Render(num) + lp.normal.Render(title)
}

// handlePreviewMouse scrolls the preview with the wheel; a click opens the PR.
func (m model) handlePreviewMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	v := m.preview
	if v == nil {
		return m, nil
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		v.panes[tabConversation].scroll -= wheelStep
	case msg.Button == tea.MouseButtonWheelDown:
		v.panes[tabConversation].scroll += wheelStep
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		return m, m.openDetail(v.pr)
	}
	v.clampScroll(m.height)
	return m, nil
}
