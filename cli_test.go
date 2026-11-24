package main

import (
	"flag"
	"slices"
	"testing"
)

func TestParseCLIFlagsMixesPositionalArgs(t *testing.T) {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	squash := fs.Bool("squash", false, "")
	max := fs.Int("max-lines", 0, "")
	pos, err := parseCLIFlags(fs, []string{"web", "--squash", "42", "--max-lines", "300"})
	if err != nil || !*squash || *max != 300 || !slices.Equal(pos, []string{"web", "42"}) {
		t.Fatalf("got %v %v %d %v", pos, *squash, *max, err)
	}
	if _, err := parseCLIFlags(fs, []string{"42", "--nope"}); err == nil {
		t.Fatal("unknown flag should fail")
	}
}

func TestRunCLIFallsThroughToTUI(t *testing.T) {
	if _, ok := runCLI(nil); ok {
		t.Fatal("no args opens the TUI")
	}
	if _, ok := runCLI([]string{"web", "42"}); ok {
		t.Fatal("a PR ref opens the TUI")
	}
}

func TestSameLoginIgnoresBotSuffix(t *testing.T) {
	if !sameLogin("greptile-apps", "greptile-apps[bot]") || !sameLogin("Max", "max") || sameLogin("max", "maxhumber") {
		t.Fatal("login matching is wrong")
	}
}
