package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// commandPalette runs any single-key action from the help list by name.
type commandPalette struct {
	query  string
	cursor int
	offset int // first visible list line
	frame  int // open animation progress, 0..paletteFrames
}

type paletteCommand struct {
	key, desc, section string
	msg                tea.KeyMsg
}

// keyMsgFor turns a help-list key into the key press it stands for. Multi-key
// entries ("j / ↓", "1-4", "#repo") are left out.
func keyMsgFor(key string) (tea.KeyMsg, bool) {
	switch key {
	case "Enter":
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}, true
	}
	if r := []rune(key); len(r) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r}, true
	}
	return tea.KeyMsg{}, false
}

func (m model) paletteCommands() []paletteCommand {
	q := strings.ToLower(m.palette.query)
	var out []paletteCommand
	secs := m.helpSections()
	sort.SliceStable(secs, func(i, j int) bool { return secs[i].title == "Actions" && secs[j].title != "Actions" })
	for _, sec := range secs {
		for _, item := range sec.items {
			msg, ok := keyMsgFor(item[0])
			if ok && strings.Contains(strings.ToLower(item[1]+" "+sec.title), q) {
				out = append(out, paletteCommand{item[0], item[1], sec.title, msg})
			}
		}
	}
	return out
}

// textInputActive is true while a prompt owns typed keys, so ctrl+k stays theirs.
func (m model) textInputActive() bool {
	if m.filterMode || m.confirmAction != "" {
		return true
	}
	v := m.detail
	return v != nil && (v.picker != nil || v.merge != nil || (v.search != nil && v.search.typing))
}

func (m model) handlePaletteInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	cmds := m.paletteCommands()
	switch msg.String() {
	case "esc", "ctrl+k":
		m.palette = nil
	case "enter":
		m.palette = nil
		if p.cursor < len(cmds) {
			return m.Update(cmds[p.cursor].msg)
		}
	case "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
	case "down", "ctrl+n", "ctrl+j":
		p.cursor = min(p.cursor+1, max(len(cmds)-1, 0))
	case "backspace", "ctrl+h":
		if p.query != "" {
			r := []rune(p.query)
			p.query = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.query += string(msg.Runes)
			p.cursor = 0
		}
	}
	return m, nil
}

const paletteFrames = 6 // ~100ms unfold on open

type paletteTickMsg struct{}

func paletteTick() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(time.Time) tea.Msg { return paletteTickMsg{} })
}

// paletteTop anchors the palette near the top so it doesn't jump as it unfolds.
func paletteTop(height int) int { return max(height/6, 1) }

// highlightMatch paints the first case-insensitive match of q in s with hl.
func highlightMatch(s, q string, base, hl painter) string {
	i := strings.Index(strings.ToLower(s), q)
	if q == "" || i < 0 {
		return base.Render(s)
	}
	return base.Render(s[:i]) + hl.Render(s[i:i+len(q)]) + base.Render(s[i+len(q):])
}

func (m model) paletteView() string {
	th := currentDiffTheme()
	p := m.palette
	q := strings.ToLower(p.query)
	width := min(64, max(m.width-4, 30))
	inner := width - 2
	listH := max(min(14, m.height-paletteTop(m.height)-6), 3)

	border := sty(th.border, "", false, false)
	muted := sty(th.muted, "", false, false)
	text := sty(th.text, "", false, false)
	accent := sty(th.accent, "", true, false)
	keycap := sty(th.text, th.chipBg, false, false)
	row := func(s string) string { return border.Render("│") + fitWidth(s, inner) + border.Render("│") }
	rule := border.Render("├" + strings.Repeat("─", inner) + "┤")

	// List lines: a header per section, then its commands.
	cmds := m.paletteCommands()
	var lines []string
	sel := 0
	for i, c := range cmds {
		if i == 0 || c.section != cmds[i-1].section {
			lines = append(lines, "  "+sty(th.muted, "", true, false).Render(strings.ToUpper(c.section)))
		}
		cap := keycap.Render(" " + c.key + " ")
		descW := inner - 4 - lipgloss.Width(cap)
		desc := truncateToWidth(c.desc, descW)
		gap := strings.Repeat(" ", max(descW-lipgloss.Width(desc), 0))
		if i == p.cursor {
			sel = len(lines)
			bg := th.branchBg
			lines = append(lines, sty(th.branchFg, bg, false, false).Render("▌ ")+
				highlightMatch(desc, q, sty(th.text, bg, true, false), sty(th.accent, bg, true, false))+
				sty("", bg, false, false).Render(gap+" ")+cap+sty("", bg, false, false).Render(" "))
		} else {
			lines = append(lines, "  "+highlightMatch(desc, q, text, accent)+gap+" "+cap+" ")
		}
	}
	if len(cmds) == 0 {
		lines = append(lines, "", "  "+muted.Render("No commands match “"+p.query+"”"))
	}

	// Keep the selection (and its section header) in view.
	top := sel
	if sel > 0 && cmds != nil && p.cursor < len(cmds) && (p.cursor == 0 || cmds[p.cursor].section != cmds[p.cursor-1].section) {
		top = sel - 1
	}
	p.offset = min(p.offset, top)
	p.offset = max(p.offset, sel-listH+1, 0)
	lines = lines[min(p.offset, len(lines)):]

	// Unfold with an ease-out: most of the height lands in the first frames.
	t := 1 - float64(p.frame)/paletteFrames
	shown := int(float64(listH)*(1-t*t*t) + 0.999)

	title := " Commands "
	out := []string{border.Render("╭─") + accent.Render(title) + border.Render(strings.Repeat("─", inner-1-lipgloss.Width(title))+"╮")}
	input := p.query + accent.Render("▏")
	if p.query == "" {
		input = accent.Render("▏") + muted.Render("Search commands…")
	}
	out = append(out, row(" "+accent.Render("❯")+" "+text.Render(input)), rule)
	for i := 0; i < shown; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, row(l))
	}

	hints := text.Render("↑↓") + muted.Render(" select  ") + text.Render("↵") + muted.Render(" run  ") + text.Render("esc") + muted.Render(" close ")
	count := muted.Render(fmt.Sprintf(" %d commands", len(cmds)))
	if len(cmds) == 1 {
		count = muted.Render(" 1 command")
	}
	if pad := inner - lipgloss.Width(count) - lipgloss.Width(hints); pad > 0 {
		hints = count + strings.Repeat(" ", pad) + hints
	}
	out = append(out, rule, row(hints), border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(out, "\n")
}
