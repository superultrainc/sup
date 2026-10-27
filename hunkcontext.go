package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// hunkAgentContext is hunk's --agent-context sidecar format.
type hunkAgentContext struct {
	Version int               `json:"version"`
	Summary string            `json:"summary,omitempty"`
	Files   []hunkFileContext `json:"files"`
}

type hunkFileContext struct {
	Path        string           `json:"path"`
	Annotations []hunkAnnotation `json:"annotations"`
}

type hunkAnnotation struct {
	OldRange  []int    `json:"oldRange,omitempty"`
	NewRange  []int    `json:"newRange,omitempty"`
	Summary   string   `json:"summary"`
	Rationale string   `json:"rationale,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Source    string   `json:"source,omitempty"`
	Author    string   `json:"author,omitempty"`
	CreatedAt string   `json:"createdAt,omitempty"`
}

// buildHunkContext maps review threads onto diff lines. Threads with no
// current line (outdated or file-level) can't be anchored and are skipped.
func buildHunkContext(d *prDetail) hunkAgentContext {
	ctx := hunkAgentContext{Version: 1, Summary: d.Title}
	byPath := map[string]int{}
	for _, t := range d.ReviewThreads.Nodes {
		if t.Line == nil || len(t.Comments.Nodes) == 0 {
			continue
		}
		end := *t.Line
		start := end
		if t.StartLine != nil && *t.StartLine <= end {
			start = *t.StartLine
		}
		// hunk's note header already names the author, so only replies get an @prefix.
		first := t.Comments.Nodes[0]
		a := hunkAnnotation{
			Summary:   cleanBody(first.Body),
			Source:    "github",
			Author:    first.Author.Login,
			CreatedAt: first.CreatedAt.Format(time.RFC3339),
		}
		if a.Summary == "" {
			a.Summary = "(empty comment)"
		}
		var replies []string
		for _, c := range t.Comments.Nodes[1:] {
			replies = append(replies, fmt.Sprintf("@%s: %s", c.Author.Login, cleanBody(c.Body)))
		}
		a.Rationale = strings.Join(replies, "\n\n")
		if t.IsResolved {
			a.Tags = append(a.Tags, "resolved")
			a.Summary = "[resolved] " + a.Summary
		}
		if t.DiffSide == "LEFT" {
			a.OldRange = []int{start, end}
		} else {
			a.NewRange = []int{start, end}
		}
		i, ok := byPath[t.Path]
		if !ok {
			i = len(ctx.Files)
			byPath[t.Path] = i
			ctx.Files = append(ctx.Files, hunkFileContext{Path: t.Path})
		}
		ctx.Files[i].Annotations = append(ctx.Files[i].Annotations, a)
	}
	return ctx
}

// writeHunkContext returns "" when there's nothing to annotate.
func writeHunkContext(d *prDetail) string {
	if d == nil {
		return ""
	}
	ctx := buildHunkContext(d)
	if len(ctx.Files) == 0 {
		return ""
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return ""
	}
	f, err := os.CreateTemp("", "sup-hunk-context-*.json")
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}
