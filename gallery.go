package main

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// imageGallery is the full-screen viewer that steps through a PR's images.
type imageGallery struct {
	refs  []imageRef
	idx   int
	lines []string // the current image, laid out for the screen
}

const fullImagePrefix = "full\x00" // imageStore key for an image at natural size

// prImages lists every image in the PR once, in the order the conversation loads them.
func prImages(d *prDetail) []imageRef {
	if d == nil {
		return nil
	}
	bodies := []string{d.Body}
	for _, c := range d.Comments.Nodes {
		bodies = append(bodies, c.Body)
	}
	for _, r := range d.Reviews.Nodes {
		bodies = append(bodies, r.Body)
	}
	for _, t := range d.ReviewThreads.Nodes {
		for _, c := range t.Comments.Nodes {
			bodies = append(bodies, c.Body)
		}
	}
	seen := map[string]bool{}
	var out []imageRef
	for _, b := range bodies {
		for _, r := range findImages(cleanBody(b)) {
			if !seen[r.url] {
				seen[r.url] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func (m model) openGallery() (tea.Model, tea.Cmd) {
	refs := prImages(m.detail.detail)
	if len(refs) == 0 {
		m.actionStatus = "No images in this PR"
		return m, nil
	}
	m.gallery = &imageGallery{refs: refs}
	return m, m.gallery.load()
}

// load fetches the current and next image at natural size.
func (g *imageGallery) load() tea.Cmd {
	var cmds []tea.Cmd
	for _, i := range []int{g.idx, (g.idx + 1) % len(g.refs)} {
		r := g.refs[i]
		key := fullImagePrefix + r.url
		if e, ok := imageStore[key]; ok && (e.loading || e.err == nil) {
			continue
		}
		imageStore[key] = &imageEntry{loading: true}
		dl := imageSources[r.url]
		if dl == "" {
			dl = r.url
		}
		j := imageJob{key: r.url, url: dl, full: true}
		cmds = append(cmds, func() tea.Msg {
			return imagesLoadedMsg{results: map[string]imageEntry{key: loadImage(j)}}
		})
	}
	return tea.Batch(cmds...)
}

func (m model) handleGalleryInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	g := m.gallery
	switch msg.String() {
	case "esc", "q", "i":
		m.gallery = nil
		return m, nil
	case "right", "l", "j", "n", " ", "down":
		g.idx = (g.idx + 1) % len(g.refs)
	case "left", "h", "k", "p", "up":
		g.idx = (g.idx + len(g.refs) - 1) % len(g.refs)
	case "o":
		if u := imageSources[g.refs[g.idx].url]; u != "" {
			exec.Command("open", "-g", u).Start()
		} else {
			exec.Command("open", "-g", g.refs[g.idx].url).Start()
		}
		return m, nil
	}
	return m, g.load()
}

// layout renders the current image as large as the screen allows, falling
// back to the inline copy while the full one loads. Update calls it so kitty
// transmissions ride the next frame.
func (g *imageGallery) layout(width, height int) {
	r := g.refs[g.idx]
	full := r
	full.url = fullImagePrefix + r.url
	e := imageStore[full.url]
	if e == nil || e.loading || e.err != nil {
		full = r
	}
	g.lines = imageLinesSized(full, width-2, height-4)
}

func (m model) galleryView() string {
	g := m.gallery
	if m.height < 5 {
		return ""
	}
	th := currentDiffTheme()
	muted := sty(th.muted, "", false, false)
	r := g.refs[g.idx]
	label := r.alt
	if label == "" {
		label = imageLabel(r.url)
	}
	status := ""
	if e := imageStore[fullImagePrefix+r.url]; e != nil && e.loading {
		status = muted.Render(" · loading full size…")
	}
	head := " " + sty(th.text, "", true, false).Render(fmt.Sprintf("Image %d of %d", g.idx+1, len(g.refs))) +
		muted.Render(" · ") + truncate(label, max(m.width-30, 10)) + status
	rows := []string{fitWidth(head, m.width), ""}
	imgW := 0
	for _, l := range g.lines {
		imgW = max(imgW, lipgloss.Width(l))
	}
	pad := spaces(max((m.width-imgW)/2, 0))
	body := m.height - 3
	top := max((body-len(g.lines))/2, 0)
	for i := 0; i < body; i++ {
		if j := i - top; j >= 0 && j < len(g.lines) {
			rows = append(rows, pad+g.lines[j])
		} else {
			rows = append(rows, "")
		}
	}
	rows = rows[:m.height-1]
	help := " ←/→ previous / next · o open in browser · esc back"
	return strings.Join(rows, "\n") + "\n" + helpStyle.Render(truncateToWidth(help, max(m.width-1, 20)))
}
