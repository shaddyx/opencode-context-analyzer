// Command opencode-context-analyzer is a TUI that inspects the opencode
// database, lists sessions, and produces a context-usage report grouped by
// tools, skills, agents, and startup context.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shaddyx/opencode-context-analyzer/internal/analyzer"
	"github.com/shaddyx/opencode-context-analyzer/internal/db"
	"github.com/shaddyx/opencode-context-analyzer/internal/tui"
)

func main() {
	var (
		dbPath     = flag.String("db", "", "path to opencode.db (default: ~/.local/share/opencode/opencode.db)")
		agentsPath = flag.String("agents", "", "path to AGENTS.md to include in startup estimate")
		skillsDir  = flag.String("skills", "", "directory containing skill subfolders with SKILL.md")
	)
	flag.Parse()

	path := *dbPath
	if path == "" {
		path = db.DefaultPath()
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "cannot open database %q: %v\n", path, err)
		os.Exit(1)
	}

	d, err := db.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()

	sources := analyzer.Sources{
		AGENTSMDPath:        resolveAgents(*agentsPath),
		SkillDirs:           resolveSkillDirs(*skillsDir),
		ToolTokensPerTool:   150,
		AgentTokensPerAgent: 250,
		SystemPromptTokens:  1200,
	}

	p := tea.NewProgram(tui.New(d, sources), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error running TUI: %v\n", err)
		os.Exit(1)
	}
}

func resolveAgents(p string) string {
	if p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".config", "opencode", "AGENTS.md"),
		filepath.Join(home, ".config", "opencode", "AGENTS.md"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func resolveSkillDirs(p string) []string {
	if p != "" {
		return []string{p}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, c := range []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".config", "opencode", "skills"),
	} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			dirs = append(dirs, c)
		}
	}
	return dirs
}
