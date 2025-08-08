package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch holds information about a git branch and its recency
// based on the most recent commit on that branch or the latest
// local work recorded in the branch reflog.
type Branch struct {
	Name        string    `json:"name"`
	CommitHash  string    `json:"commitHash"`
	CommitTime  time.Time `json:"commitTime"`
	IsCurrent   bool      `json:"isCurrent"`
	HasUpstream bool      `json:"hasUpstream"`
}

func main() {
	limit := 10
	jsonOut := false
	doFetch := false
	forceTUI := false
	disableTUI := false

	// Basic flag parsing without external deps
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-n" || arg == "--n" || strings.HasPrefix(arg, "--n=") || strings.HasPrefix(arg, "-n=") || strings.HasPrefix(arg, "--limit=") || strings.HasPrefix(arg, "-limit=") {
			var val string
			if strings.Contains(arg, "=") {
				parts := strings.SplitN(arg, "=", 2)
				val = parts[1]
			} else {
				if i+1 < len(os.Args) {
					val = os.Args[i+1]
					i++
				} else {
					fatal("-n flag requires a value")
				}
			}
			v, err := strconv.Atoi(val)
			if err != nil || v <= 0 {
				fatal("invalid -n value: %s", val)
			}
			limit = v
			continue
		}
		if arg == "-json" || arg == "--json" {
			jsonOut = true
			continue
		}
		if arg == "--fetch" {
			doFetch = true
			continue
		}
		if arg == "--tui" {
			forceTUI = true
			continue
		}
		if arg == "--no-tui" {
			disableTUI = true
			continue
		}
		if arg == "-h" || arg == "--help" || arg == "help" {
			usage()
			return
		}
	}

	if !isGitRepo() {
		fatal("not a git repository (or any of the parent directories): .git")
	}

	if doFetch {
		_ = gitFetchAll()
	}

	branches, err := getRecentBranches()
	if err != nil {
		fatal("%v", err)
	}

	if len(branches) > limit {
		branches = branches[:limit]
	}

	// Prefer JSON output if requested
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(branches)
		return
	}

	// Auto-run TUI if stdout is a terminal, unless disabled explicitly
	if (forceTUI || (isTerminal(os.Stdout.Fd()) && !disableTUI)) && !jsonOut {
		if err := runTUI(branches); err != nil {
			fatal("%v", err)
		}
		return
	}

	// Fallback plain output
	for _, b := range branches {
		current := " "
		if b.IsCurrent {
			current = "*"
		}
		hash := b.CommitHash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		fmt.Printf("%s %-30s  %s  %s\n", current, b.Name, hash, humanizeTime(b.CommitTime))
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "grecent - list recent git branches by last activity\n\n")
	fmt.Fprintf(os.Stderr, "Usage: grecent [-n N] [--json] [--fetch] [--tui|--no-tui]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -n, --limit N   Limit number of branches (default 10)\n")
	fmt.Fprintf(os.Stderr, "  --json          Output JSON\n")
	fmt.Fprintf(os.Stderr, "  --fetch         Run 'git fetch --all --prune --tags' first to refresh remotes\n")
	fmt.Fprintf(os.Stderr, "  --tui           Force TUI mode\n")
	fmt.Fprintf(os.Stderr, "  --no-tui        Disable TUI even if stdout is a terminal\n")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Stderr = new(bytes.Buffer)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func gitFetchAll() error {
	cmd := exec.Command("git", "fetch", "--all", "--prune", "--tags", "--quiet")
	cmd.Stdout = new(bytes.Buffer)
	cmd.Stderr = new(bytes.Buffer)
	return cmd.Run()
}

func getRecentBranches() ([]Branch, error) {
	// Strategy:
	// - Use for-each-ref to get local branches with their HEAD commit and committerdate
	// - Identify current branch
	// - For each branch, consider the latest reflog entry time for local work
	// - Also consider upstream remote tip time (batch fetched) to approximate last push/fetch activity
	// - Sort by the max(committerdate, reflogTime, remoteTipTime) desc
	format := "%(refname:short)\t%(objectname)\t%(committerdate:iso-strict)\t%(upstream)\n"
	cmd := exec.Command(
		"git", "for-each-ref",
		"--sort=-committerdate",
		"--format", format,
		"refs/heads",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref failed: %w", err)
	}

	currentBranch, _ := getCurrentBranch()

	remoteTimes := getRemoteBranchTimes()

	scanner := bufio.NewScanner(bytes.NewReader(out))
	branches := make([]Branch, 0, 32)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		commit := fields[1]
		commitTimeStr := fields[2]
		upstreamRaw := fields[3]

		commitTime, err := time.Parse(time.RFC3339, strings.TrimSpace(commitTimeStr))
		if err != nil {
			commitTime, err = parseGitDate(commitTimeStr)
			if err != nil {
				return nil, fmt.Errorf("parse date for %s: %w", name, err)
			}
		}

		// Consider branch reflog latest entry time as "worked on" signal
		if t, ok := getBranchReflogLatestTime(name); ok && t.After(commitTime) {
			commitTime = t
		}

		// Consider remote tip time for upstream branch if available
		upstreamShort := normalizeUpstream(upstreamRaw)
		if upstreamShort != "" {
			if rt, ok := remoteTimes[upstreamShort]; ok && rt.After(commitTime) {
				commitTime = rt
			}
		}

		branches = append(branches, Branch{
			Name:        name,
			CommitHash:  commit,
			CommitTime:  commitTime,
			IsCurrent:   name == currentBranch,
			HasUpstream: upstreamShort != "",
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort by computed activity time desc
	sort.SliceStable(branches, func(i, j int) bool {
		return branches[i].CommitTime.After(branches[j].CommitTime)
	})

	return branches, nil
}

func getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "", errors.New("detached HEAD")
	}
	return branch, nil
}

func getBranchReflogLatestTime(branch string) (time.Time, bool) {
	// Ask for latest reflog entry on the branch ref
	// The %gd token includes the date in the reflog selector when --date=iso-strict
	cmd := exec.Command(
		"git", "reflog",
		"--date=iso-strict",
		"--pretty=%gd",
		"-n", "1",
		"refs/heads/"+branch,
	)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return time.Time{}, false
	}
	line := strings.TrimSpace(string(out))
	// Example: refs/heads/feature@{2025-08-07T11:35:23+02:00}
	start := strings.Index(line, "@{")
	end := strings.LastIndex(line, "}")
	if start == -1 || end == -1 || start+2 >= end {
		return time.Time{}, false
	}
	dateStr := line[start+2 : end]
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t, true
	}
	// lenient fallback
	if t, err := parseGitDate(dateStr); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func getRemoteBranchTimes() map[string]time.Time {
	// Batch query remote branches tip committer dates
	cmd := exec.Command(
		"git", "for-each-ref",
		"--format", "%(refname:short)\t%(committerdate:iso-strict)",
		"refs/remotes",
	)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return map[string]time.Time{}
	}
	result := make(map[string]time.Time, 64)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0]) // e.g., origin/feature
		dateStr := strings.TrimSpace(parts[1])
		if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
			result[name] = t
			continue
		}
		if t, err := parseGitDate(dateStr); err == nil {
			result[name] = t
		}
	}
	return result
}

func normalizeUpstream(us string) string {
	us = strings.TrimSpace(us)
	if us == "" {
		return ""
	}
	// Sometimes prints as refs/remotes/origin/branch
	if strings.HasPrefix(us, "refs/remotes/") {
		return strings.TrimPrefix(us, "refs/remotes/")
	}
	return us // already like origin/branch
}

func parseGitDate(s string) (time.Time, error) {
	// git may output like: 2023-10-01 12:34:56 +0000
	// Try a few common formats
	layouts := []string{
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 -07:00",
		"Mon Jan 2 15:04:05 2006 -0700",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date: %s", s)
}

func humanizeTime(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	if d < 30*24*time.Hour {
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	if d < 365*24*time.Hour {
		months := int(d.Hours() / (24 * 30))
		if months <= 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", months)
	}
	years := int(d.Hours() / (24 * 365))
	if years <= 1 {
		return "1y ago"
	}
	return fmt.Sprintf("%dy ago", years)
}
