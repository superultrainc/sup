# spr

A terminal UI for browsing and checking out GitHub PRs.

![spr screenshot](screenshot.png)

## Install

```bash
# Using go install
go install github.com/yourusername/spr@latest

# Or clone and build
git clone https://github.com/yourusername/spr
cd spr && ./install.sh
```

## Usage

```bash
spr          # Show PRs from your GitHub organizations (auto-detected)
spr --mine   # Show PRs you're involved in (authored, reviewing, mentioned)
```

**Zero config required** - spr automatically detects your GitHub organizations.

Select a PR and press Enter to check it out locally.

## Keybindings

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `g` | Go to top |
| `G` | Go to bottom |
| `/` | Filter PRs |
| `o` | Open PR in browser |
| `Enter` | Checkout PR |
| `q` / `Esc` | Quit |

## Configuration (optional)

| Variable | Description | Default |
|----------|-------------|---------|
| `SPR_ORG` | Override org detection (comma-separated) | auto-detected |
| `SPR_DEV_DIR` | Override repo location search | auto-detected |

Repos are automatically found in: `~/Development`, `~/dev`, `~/projects`, `~/code`, `~/src`, `~/repos`, `~/github`, `~/git`, `~`

## Requirements

- [gh](https://cli.github.com/) CLI (authenticated via `gh auth login`)
- Go 1.21+ (for building)
