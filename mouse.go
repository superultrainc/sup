package main

import tea "github.com/charmbracelet/bubbletea"

const wheelStep = 3

// listFirstRow is the screen row of the first PR: blank, filter, header, rule.
const listFirstRow = 4

// listStart is the index of the first visible PR; it mirrors View's windowing.
func (m model) listStart() int {
	visible := m.height - 8
	if visible < 5 {
		visible = 15
	}
	if m.cursor >= visible {
		return m.cursor - visible + 1
	}
	return 0
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.helpMode || m.palette != nil {
		return m, nil
	}
	if m.detail != nil {
		return m.handleDetailMouse(msg)
	}
	if m.filterMode || m.confirmAction != "" {
		return m, nil
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.cursor = max(m.cursor-1, 0)
		return m, m.schedulePrefetch()
	case msg.Button == tea.MouseButtonWheelDown:
		m.cursor = max(min(m.cursor+1, len(m.filtered)-1), 0)
		return m, m.schedulePrefetch()
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		i := m.listStart() + msg.Y - listFirstRow
		if msg.Y < listFirstRow || i < 0 || i >= len(m.filtered) {
			return m, nil
		}
		// First click selects; clicking the selected row opens it.
		if i == m.cursor {
			return m, m.openDetail(m.filtered[i])
		}
		m.cursor = i
		return m, m.schedulePrefetch()
	}
	return m, nil
}

func (m model) handleDetailMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	v := m.detail
	if v.picker != nil || v.merge != nil {
		return m, nil
	}
	p := v.pane()
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		p.scroll -= wheelStep
	case msg.Button == tea.MouseButtonWheelDown:
		p.scroll += wheelStep
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		const tabRow = 2
		if msg.Y == tabRow {
			for i, span := range v.tabSpans() {
				if msg.X >= span[0] && msg.X < span[1] {
					v.tab = i
				}
			}
			return m, nil
		}
		// File tree: jump the diff to the clicked file.
		if v.tab == tabFiles && v.treeWidth() > 0 && msg.X < v.treeWidth() && msg.Y >= detailHeaderHeight {
			_, fileAt := fileTree(v.diff, v.currentFile(), v.detail, v.treeWidth(), v.bodyHeight(m.height))
			row := msg.Y - detailHeaderHeight
			if row < len(fileAt) && fileAt[row] >= 0 && fileAt[row] < len(v.panes[tabFiles].marks) {
				v.panes[tabFiles].scroll = v.panes[tabFiles].marks[fileAt[row]]
			}
		}
	}
	v.clampScroll(m.height)
	return m, nil
}
