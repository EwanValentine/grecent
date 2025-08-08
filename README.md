## grecent

A Go CLI that shows your most recently worked-on Git branches in the current repo. It ranks branches by recent activity using a combination of:

- Last commit time on the branch
- Latest local work (branch reflog time)
- Upstream remote tip time (approx last push/fetch)

Includes a modern TUI (Bubble Tea + Lip Gloss) with fuzzy search, sorting, and quick actions.

### Features
- Rank local branches by recent activity
- Optional refresh of remotes (`--fetch`)
- JSON output for scripting (`--json`)
- TUI by default when running in a terminal (can force/disable)
- Fuzzy search, sort by name/time, and quick actions (checkout, delete, merge)

### Install

- From source (recommended):
  ```fish
  go install github.com/EwanValentine/grecent@latest
  ```
  Ensure your `GOPATH/bin` is on `PATH`. On fish:
  ```fish
  fish_add_path (go env GOPATH)/bin
  ```

- Local build (for contributors):
  ```fish
  git clone https://github.com/EwanValentine/grecent.git
  cd grecent
  go build -o grecent .
  ./grecent --help
  ```

### Usage

- Basic:
  ```fish
  # In any git repo
  grecent
  ```

- JSON output:
  ```fish
  grecent --json | jq
  ```

- Limit results:
  ```fish
  grecent -n 20
  ```

- Refresh remotes before ranking:
  ```fish
  grecent --fetch
  ```

- TUI control:
  ```fish
  grecent --tui      # force TUI
  grecent --no-tui   # disable TUI
  ```

### TUI keybindings
- Navigation: j/k or ↑/↓, g/G for top/bottom
- Search: `/` then type your query (fuzzy), Enter to apply, Esc to clear
- Sort: `s` cycles between time/name asc/desc
- Refresh: `r` recompute locally; `f` fetch remotes then recompute
- Actions: Enter to checkout; `x` delete (with confirmation); `m` merge into current (with confirmation)
- Quit: `q` or Ctrl+C

### How recency is computed
For each local branch, we compute the latest of:
- The branch HEAD commit time
- The latest branch reflog entry time (`git reflog refs/heads/<branch> --date=iso-strict`)
- If an upstream exists, the remote tip committer date (via `git for-each-ref refs/remotes/...`)

Branches are then sorted by this computed time (most recent first).

### CLI options
- `-n, --limit` N: limit number of branches (default 10)
- `--json`: print JSON instead of TUI/plain text
- `--fetch`: run `git fetch --all --prune --tags` before ranking
- `--tui`: force TUI
- `--no-tui`: disable TUI (even if stdout is a TTY)

### Troubleshooting
- "not a git repository": run inside a repo (or any subdirectory)
- PATH issues after `go install`: ensure `(go env GOPATH)/bin` is on PATH
- Slow remote times: use `--fetch` to refresh remotes if needed
- Merge conflicts when using `m`: resolve with normal git workflow

### Contributing
- Issues and PRs welcome at `github.com/EwanValentine/grecent`.
- Run `go mod tidy && go build` before submitting.

### License
MIT (see LICENSE).