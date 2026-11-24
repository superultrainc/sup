package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Headless subcommands for scripts and agents. Each command's file registers itself here.
var cliCommands = map[string]func(args []string) int{}

const (
	exitOK      = 0
	exitError   = 1 // GitHub or network failure
	exitUsage   = 2
	exitRefused = 3 // a guard refused the action
	exitTimeout = 4
)

// runCLI runs a subcommand when args name one; ok is false to fall through to the TUI.
func runCLI(args []string) (code int, ok bool) {
	if len(args) == 0 {
		return 0, false
	}
	cmd, ok := cliCommands[args[0]]
	if !ok {
		return 0, false
	}
	return cmd(args[1:]), true
}

// parseCLIFlags lets flags and positional args mix, as in `sup merge 42 --squash`.
func parseCLIFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// cliPR resolves a PR ref the way the TUI's arguments do, and loads the gh token.
func cliPR(args []string) (PR, error) {
	meta, _ := loadCachedMeta()
	pr, err := parsePRArgs(args, loadCachedPRs(), meta.Orgs)
	if err != nil {
		return pr, err
	}
	if err := loadGHToken(); err != nil {
		return pr, fmt.Errorf("failed to read gh auth token. Run: gh auth login")
	}
	return pr, nil
}

func cliFail(code int, format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "sup: "+format+"\n", a...)
	return code
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// sameLogin matches GraphQL bot logins ("greptile-apps") against REST ones ("greptile-apps[bot]").
func sameLogin(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "[bot]"), strings.TrimSuffix(b, "[bot]"))
}
