package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// checkPatch verifies every hunk header's line counts match its body.
func checkPatch(patch string) error {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	files := 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "diff --git ") {
			files++
			continue
		}
		if !strings.HasPrefix(l, "@@ ") {
			continue
		}
		var os, oc, ns, nc int
		if _, err := fmt.Sscanf(l, "@@ -%d,%d +%d,%d @@", &os, &oc, &ns, &nc); err != nil {
			return fmt.Errorf("bad hunk header %q (use the -a,b +c,d form): %v", l, err)
		}
		gotOld, gotNew := 0, 0
		for i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "@@ ") && !strings.HasPrefix(lines[i+1], "diff --git ") {
			i++
			switch b := lines[i]; {
			case strings.HasPrefix(b, "+"):
				gotNew++
			case strings.HasPrefix(b, "-"):
				gotOld++
			case strings.HasPrefix(b, " "), b == "":
				gotOld++
				gotNew++
			case strings.HasPrefix(b, `\`):
			default:
				return fmt.Errorf("hunk %q has a line without a +, - or space prefix: %q", l, b)
			}
		}
		if gotOld != oc || gotNew != nc {
			return fmt.Errorf("hunk %q has %d old / %d new lines", l, gotOld, gotNew)
		}
	}
	if files == 0 {
		return fmt.Errorf("no files")
	}
	return nil
}

func TestDemoContent(t *testing.T) {
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	people := map[string]bool{"sarah": true, "mike": true, "alex": true, "jordan": true, "chris": true, "taylor": true, "sam": true, "priya": true}
	keys := map[string]bool{}
	for _, pr := range mockPRs() {
		key := pr.Repository.Name + "#" + strconv.Itoa(pr.Number)
		keys[key] = true
		c, ok := demoContents[key]
		if !ok {
			t.Errorf("%s: no demo content", key)
			continue
		}
		if strings.TrimSpace(c.body) == "" || len(c.commits) == 0 {
			t.Errorf("%s: needs a body and at least one commit", key)
		}
		if err := checkPatch(c.patch); err != nil {
			t.Errorf("%s: patch: %v", key, err)
		}
		if files := parseUnifiedDiff(c.patch); len(files) == 0 {
			t.Errorf("%s: patch doesn't parse", key)
		}
		for _, cm := range c.comments {
			if !people[cm.author] {
				t.Errorf("%s: unknown comment author %q", key, cm.author)
			}
		}
		if th := c.thread; th.path != "" {
			if !people[th.author] {
				t.Errorf("%s: unknown thread author %q", key, th.author)
			}
			h := demoHunk(c.patch, th.path, th.line)
			if h == "" {
				t.Errorf("%s: thread line %d isn't a new-side line of %s in the patch", key, th.line, th.path)
			}
		}
	}
	for key, c := range demoContents {
		texts := []string{c.body}
		for _, cm := range c.comments {
			texts = append(texts, cm.body)
		}
		for _, text := range texts {
			for _, ref := range findImages(text) {
				if name, ok := strings.CutPrefix(ref.url, "demo:"); !ok || demoImage(name) == nil {
					t.Errorf("%s: image %q isn't a drawn demo image", key, ref.url)
				}
			}
		}
	}
	for key := range demoContents {
		if !keys[key] {
			t.Errorf("%s: content for a PR that isn't in the demo", key)
		}
	}
}
