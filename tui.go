package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lithammer/fuzzysearch/fuzzy"
)

// Safe terminal detection without external deps
func isTerminal() bool {
	// Basic heuristic: if stdout is not a character device, likely not a TTY.
	// On macOS and most Unix, os.Stdout.Stat().Mode()&os.ModeCharDevice != 0 indicates a TTY.
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// TUI model

type model struct {
	branches []Branch
	filtered []Branch
	cursor   int
	width    int
	height   int
	search   string
	status   string
	sortBy   string // name|time
	sortDesc bool

	confirming bool
	action     string // "delete" | "merge"
}

func initialModel(branches []Branch) model {
	m := model{
		branches: branches,
		filtered: make([]Branch, len(branches)),
		cursor:   0,
		search:   "",
		sortBy:   "time",
		sortDesc: true,
	}
	copy(m.filtered, branches)
	m.applySortFilter()
	return m
}

func runTUI(branches []Branch) error {
	p := tea.NewProgram(initialModel(branches), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Messages

type tickMsg time.Time

type statusMsg string

// Update/View

func (m model) Init() tea.Cmd {
	return tea.Batch(tick(), status("j/k or ↑/↓ move • / search (fuzzy) • s sort • r refresh • f fetch • enter checkout • x delete • m merge into current • q quit"))
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func status(s string) tea.Cmd { return func() tea.Msg { return statusMsg(s) } }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		s := msg.String()

		// Handle confirmation state
		if m.confirming {
			switch s {
			case "y", "Y":
				if m.cursor >= 0 && m.cursor < len(m.filtered) {
					b := m.filtered[m.cursor]
					if m.action == "delete" {
						if b.IsCurrent {
							m.status = "cannot delete current branch"
						} else if err := gitDeleteBranch(b.Name); err != nil {
							m.status = fmt.Sprintf("delete failed: %v", err)
						} else {
							m.status = fmt.Sprintf("deleted %s", b.Name)
							brs, err := getRecentBranches()
							if err == nil {
								m.branches = brs
								m.applySortFilter()
							}
						}
					} else if m.action == "merge" {
						if b.IsCurrent {
							m.status = "already on this branch"
						} else if err := gitMergeIntoCurrent(b.Name); err != nil {
							m.status = fmt.Sprintf("merge failed: %v", err)
						} else {
							m.status = fmt.Sprintf("merged %s into current", b.Name)
							brs, err := getRecentBranches()
							if err == nil {
								m.branches = brs
								m.applySortFilter()
							}
						}
					}
				}
				m.confirming = false
				m.action = ""
				return m, nil
			case "n", "N", "esc":
				m.status = "cancelled"
				m.confirming = false
				m.action = ""
				return m, nil
			}
		}

		switch s {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			if len(m.filtered) > 0 {
				m.cursor = len(m.filtered) - 1
			}
		case "/":
			m.status = "Search: type to filter, press Enter to apply, Esc to clear"
			return m, readLine(&m.search)
		case "esc":
			m.search = ""
			m.applySortFilter()
		case "enter":
			if m.cursor >= 0 && m.cursor < len(m.filtered) {
				b := m.filtered[m.cursor]
				if err := gitCheckoutBranch(b.Name); err != nil {
					m.status = fmt.Sprintf("checkout failed: %v", err)
				} else {
					m.status = fmt.Sprintf("checked out %s", b.Name)
				}
			}
		case "x", "delete":
			if m.cursor >= 0 && m.cursor < len(m.filtered) {
				b := m.filtered[m.cursor]
				m.confirming = true
				m.action = "delete"
				m.status = fmt.Sprintf("delete %s? y/N", b.Name)
			}
		case "m":
			if m.cursor >= 0 && m.cursor < len(m.filtered) {
				b := m.filtered[m.cursor]
				m.confirming = true
				m.action = "merge"
				m.status = fmt.Sprintf("merge %s into current? y/N", b.Name)
			}
		case "r":
			brs, err := getRecentBranches()
			if err != nil {
				m.status = fmt.Sprintf("refresh failed: %v", err)
				return m, nil
			}
			m.branches = brs
			m.applySortFilter()
			m.status = "refreshed"
		case "f":
			_ = gitFetchAll()
			brs, err := getRecentBranches()
			if err != nil {
				m.status = fmt.Sprintf("fetch failed: %v", err)
				return m, nil
			}
			m.branches = brs
			m.applySortFilter()
			m.status = "fetched"
		case "s":
			// toggle sort: time desc -> name asc -> time asc -> name desc ...
			if m.sortBy == "time" {
				m.sortBy = "name"
				m.sortDesc = false
			} else if m.sortBy == "name" && !m.sortDesc {
				m.sortBy = "time"
				m.sortDesc = false
			} else if m.sortBy == "time" && !m.sortDesc {
				m.sortBy = "name"
				m.sortDesc = true
			} else {
				m.sortBy = "time"
				m.sortDesc = true
			}
			m.applySortFilter()
		}
	case tickMsg:
		return m, tea.Batch(tick())
	case statusMsg:
		m.status = string(msg)
	}
	return m, nil
}

func (m *model) applySortFilter() {
	m.filtered = m.filtered[:0]
	if m.search == "" {
		m.filtered = append(m.filtered, m.branches...)
	} else {
		// Fuzzy filter by branch name, preserving library order
		names := make([]string, len(m.branches))
		nameToBranch := make(map[string]Branch, len(m.branches))
		for i, b := range m.branches {
			n := b.Name
			names[i] = n
			nameToBranch[n] = b
		}
		matches := fuzzy.FindNormalizedFold(m.search, names)
		for _, n := range matches {
			if br, ok := nameToBranch[n]; ok {
				m.filtered = append(m.filtered, br)
			}
		}
	}

	// If no active search, apply chosen sort
	if m.search == "" {
		sort.SliceStable(m.filtered, func(i, j int) bool {
			if m.sortBy == "name" {
				if m.sortDesc {
					return m.filtered[i].Name > m.filtered[j].Name
				}
				return m.filtered[i].Name < m.filtered[j].Name
			}
			// sort by time
			if m.sortDesc {
				return m.filtered[i].CommitTime.After(m.filtered[j].CommitTime)
			}
			return m.filtered[i].CommitTime.Before(m.filtered[j].CommitTime)
		})
	}

	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m model) View() string {
	styleTitle := lipgloss.NewStyle().Bold(true)
	styleHeader := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	styleCurrent := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	var b strings.Builder
	b.WriteString(styleTitle.Render("grecent - recent branches") + "\n")
	b.WriteString(styleHeader.Render("j/k,↑/↓ move • / search (fuzzy) • s sort • r refresh • f fetch • enter checkout • x delete • m merge • q quit") + "\n\n")
	if m.search != "" {
		b.WriteString(styleHeader.Render("filter: ") + m.search + "\n")
	}

	// Table header: Branch, Hash, Age, Date, Upstream
	b.WriteString(styleHeader.Render(fmt.Sprintf("%-2s %-32s %-8s %-8s %-20s %-20s\n", "", "Branch", "Hash", "Age", "Date", "Upstream")))
	b.WriteString(styleHeader.Render(strings.Repeat("─", 96)) + "\n")

	for i, br := range m.filtered {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("→ ")
		}
		name := br.Name
		if br.IsCurrent {
			name = styleCurrent.Render("* " + name)
		}
		hash := br.CommitHash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		age := humanizeTime(br.CommitTime)
		date := br.CommitTime.Format("2006-01-02 15:04")
		up := ""
		if br.HasUpstream {
			up = "yes"
		}
		row := fmt.Sprintf("%s%-32s %-8s %-8s %-20s %-20s\n", cursor, name, hash, age, date, up)
		b.WriteString(row)
	}

	if m.status != "" {
		b.WriteString("\n" + styleHeader.Render(m.status) + "\n")
	}

	return b.String()
}

// Simple line reader prompt for search
func readLine(target *string) tea.Cmd {
	return func() tea.Msg {
		fmt.Print("search> ")
		var s string
		_, _ = fmt.Scanln(&s)
		*target = s
		return statusMsg("filter applied")
	}
}

func gitCheckoutBranch(name string) error {
	return runCmdSilent("git", "checkout", name)
}

func gitDeleteBranch(name string) error {
	// Try safe delete; if it fails, attempt force delete
	if err := runCmdSilent("git", "branch", "-d", name); err != nil {
		return runCmdSilent("git", "branch", "-D", name)
	}
	return nil
}

func gitMergeIntoCurrent(name string) error {
	// Merge selected branch into current HEAD
	return runCmdSilent("git", "merge", name)
}

func runCmdSilent(name string, args ...string) error {
	cmd := newCommand(name, args...)
	return cmd.Run()
}

// newCommand wraps exec.Command for testing/mocking if needed
func newCommand(name string, args ...string) *exec.Cmd {
	return execCommand(name, args...)
}

var execCommand = func(name string, args ...string) *exec.Cmd {
	return osCommand(name, args...)
}

var osCommand = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
