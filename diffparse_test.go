package main

import (
	"reflect"
	"testing"
)

func TestParseUnifiedDiff(t *testing.T) {
	files := parseUnifiedDiff(mockPatch)
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d", len(files))
	}

	f := files[0]
	if f.path() != "internal/thing/thing.go" || f.status != "modified" || len(f.hunks) != 2 {
		t.Fatalf("thing.go: %+v", f)
	}
	if f.additions != 6 || f.deletions != 3 {
		t.Errorf("thing.go counts: +%d -%d", f.additions, f.deletions)
	}
	// Blank context lines in the mock have no leading space; numbering must still line up.
	h := f.hunks[1]
	last := h.lines[len(h.lines)-1]
	if last.text != "}" || last.oldNo != 45 || last.newNo != 48 {
		t.Errorf("last line of hunk 2: %+v", last)
	}
	for _, l := range h.lines {
		if l.newNo == 42 && l.kind != lineAdd {
			t.Errorf("line 42 should be an addition, got %+v", l)
		}
	}

	if files[1].status != "added" || files[1].oldPath != "" || files[1].additions != 12 {
		t.Errorf("added file: %+v", files[1])
	}
	if files[1].hunks[0].lines[5].text != "    if _, err := NewThing(Config{}); err == nil {" {
		t.Errorf("tabs should expand to 4 spaces: %q", files[1].hunks[0].lines[5].text)
	}
}

func TestParseRenameAndDelete(t *testing.T) {
	patch := "diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n" +
		"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n" +
		"diff --git a/img.png b/img.png\nBinary files a/img.png and b/img.png differ\n"
	files := parseUnifiedDiff(patch)
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	if files[0].status != "renamed" || files[0].oldPath != "old.go" || files[0].newPath != "new.go" {
		t.Errorf("rename: %+v", files[0])
	}
	if files[1].status != "deleted" || files[1].path() != "gone.txt" || files[1].deletions != 1 {
		t.Errorf("delete: %+v", files[1])
	}
	if !files[2].binary {
		t.Errorf("binary not detected")
	}
}

func TestWordDiff(t *testing.T) {
	a := `	mux.HandleFunc("/things", legacyThings)`
	b := `	mux.HandleFunc("/things", withTimeout(things, 5*time.Second))`
	as, bs := wordDiff(a, b)
	if got := spanText(a, as); !reflect.DeepEqual(got, []string{"legacyThings"}) {
		t.Errorf("old spans: %q", got)
	}
	if got := spanText(b, bs); !reflect.DeepEqual(got, []string{"withTimeout(things, 5*time.Second)"}) {
		t.Errorf("new spans: %q", got)
	}

	// Mostly-different lines get no word highlights.
	if as, bs := wordDiff("return a + b", "log.Fatal(err)"); as != nil || bs != nil {
		t.Errorf("unrelated lines should not be word-diffed: %v %v", as, bs)
	}
}

func spanText(s string, spans []span) []string {
	var out []string
	for _, sp := range spans {
		out = append(out, s[sp.start:sp.end])
	}
	return out
}
