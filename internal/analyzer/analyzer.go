// Package analyzer inspects a session's parts and messages to build a
// comprehensive context-usage report grouped by tools, skills, agents, etc.
package analyzer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaddyx/opencode-context-analyzer/internal/mcp"
	"github.com/shaddyx/opencode-context-analyzer/internal/tokenizer"
)

// ToolUsage aggregates usage for a single tool.
type ToolUsage struct {
	Name        string
	Calls       int
	InputTokens int
	OutputTokens int
	Status      map[string]int
}

// SkillUsage aggregates usage for a single skill.
type SkillUsage struct {
	Name        string
	Loads       int
	Tokens      int
}

// AgentUsage aggregates usage for a single agent.
type AgentUsage struct {
	Name        string
	Messages    int
	Tokens      int
}

// Group is a named section of the report.
type Group struct {
	Name    string
	Total   int
	Entries []Entry
}

// Entry is a single row within a group.
type Entry struct {
	Name  string
	Count int
	Tokens int
	Detail string
}

// Report is the full analysis of a session.
type Report struct {
	SessionID   string
	SessionTitle string
	Directory   string
	Model       string
	Agent       string

	// Totals
	TotalTokens      int
	TotalToolCalls   int
	TotalMessages    int
	TotalSkills      int
	TotalAgents      int
	MessageTokens    int

	// Groups
	Tools  []ToolUsage
	Skills []SkillUsage
	Agents []AgentUsage

	// Startup context estimates
	Startup StartupContext

	// Raw conversation stats
	UserMessages   int
	AssistantMessages int
	ReasoningParts int
	TextParts      int
	FileParts      int
	PatchParts     int

	// RealStartupTokens is the provider-reported input token count of the
	// session's first assistant message (0 if unknown). It is the ground
	// truth for the startup context size.
	RealStartupTokens int
}

// StartupContext estimates the fixed context loaded at session start.
type StartupContext struct {
	SystemPromptTokens int
	AGENTSMDTokens     int
	SkillTokens        int
	ToolTokens         int
	AgentTokens        int
	MCPTokens          int
	Total              int

	// Detailed per-category items for subtable rendering.
	SystemPrompt []StartupItem
	AGENTSMD     []StartupItem
	Skills       []StartupItem
	Tools        []StartupItem
	Agents       []StartupItem
	MCP          []StartupItem
}

// StartupItem is a single row within a startup-context category subtable.
type StartupItem struct {
	Name             string
	Description      string
	DescriptionTokens int
	ContentTokens    int
	LoadedTokens     int
	Percent          float64
}

// Sources describes the on-disk context sources loaded at startup.
type Sources struct {
	// AGENTSMDPath is the path to the AGENTS.md file (optional).
	AGENTSMDPath string
	// SkillDirs are directories containing SKILL.md files (optional).
	SkillDirs []string
	// ConfigPath is the opencode JSONC config file whose "mcp" section is
	// used to discover MCP servers and their tool definitions (optional).
	ConfigPath string
	// AgentsDir is the directory containing agent definition .md files
	// (optional).
	AgentsDir string
	// MCPTimeout bounds each MCP server's tool discovery.
	MCPTimeout time.Duration
	// SystemPromptTokens is the heuristic baseline for the opencode core
	// system prompt text.
	SystemPromptTokens int
}

// partRow is a raw part row from the database.
type partRow struct {
	ID        string
	MessageID string
	Data      string
}

// Analyze loads and analyzes a single session.
func Analyze(d *sql.DB, sessionID string, src Sources) (*Report, error) {
	rep := &Report{SessionID: sessionID}

	// Session metadata
	if err := loadSessionMeta(d, rep); err != nil {
		return nil, err
	}

	// Load all parts
	parts, err := loadParts(d, sessionID)
	if err != nil {
		return nil, err
	}

	// Load messages for agent/message stats
	if err := loadMessages(d, sessionID, rep); err != nil {
		return nil, err
	}

	// Analyze parts
	analyzeParts(parts, rep)

	// Estimate startup context
	estimateStartup(rep, src)

	// Sort groups
	sortTools(rep.Tools)
	sort.Slice(rep.Skills, func(i, j int) bool { return rep.Skills[i].Tokens > rep.Skills[j].Tokens })
	sort.Slice(rep.Agents, func(i, j int) bool { return rep.Agents[i].Tokens > rep.Agents[j].Tokens })

	rep.TotalTokens = rep.Startup.Total
	for _, t := range rep.Tools {
		rep.TotalTokens += t.InputTokens + t.OutputTokens
	}
	rep.TotalTokens += rep.MessageTokens
	rep.TotalSkills = len(rep.Skills)
	rep.TotalAgents = len(rep.Agents)

	return rep, nil
}

func loadSessionMeta(d *sql.DB, rep *Report) error {
	row := d.QueryRow(`
		SELECT title, directory, model, agent
		FROM session WHERE id = ?
	`, rep.SessionID)
	var title, dir, model, agent sql.NullString
	if err := row.Scan(&title, &dir, &model, &agent); err != nil {
		return fmt.Errorf("load session meta: %w", err)
	}
	rep.SessionTitle = title.String
	rep.Directory = dir.String
	rep.Model = model.String
	rep.Agent = agent.String

	// Provider-reported input tokens of the first assistant message: the
	// actual startup context size (system prompt + tools + AGENTS.md +
	// skills + agents + first user message).
	var real int
	err := d.QueryRow(`
		SELECT COALESCE(json_extract(data, '$.tokens.input'), 0)
		FROM message
		WHERE session_id = ?
		  AND json_extract(data, '$.role') = 'assistant'
		  AND COALESCE(json_extract(data, '$.tokens.input'), 0) > 0
		ORDER BY time_created ASC
		LIMIT 1
	`, rep.SessionID).Scan(&real)
	if err == nil {
		rep.RealStartupTokens = real
	}
	return nil
}

func loadParts(d *sql.DB, sessionID string) ([]partRow, error) {
	rows, err := d.Query(`
		SELECT id, message_id, data FROM part WHERE session_id = ? ORDER BY time_created ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load parts: %w", err)
	}
	defer rows.Close()
	var parts []partRow
	for rows.Next() {
		var p partRow
		if err := rows.Scan(&p.ID, &p.MessageID, &p.Data); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

func loadMessages(d *sql.DB, sessionID string, rep *Report) error {
	rows, err := d.Query(`
		SELECT data FROM message WHERE session_id = ? ORDER BY time_created ASC
	`, sessionID)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()
	agentMap := map[string]*AgentUsage{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return err
		}
		rep.TotalMessages++
		rep.MessageTokens += tokenizer.Estimate(data)
		var m struct {
			Role  string `json:"role"`
			Agent string `json:"agent"`
		}
		_ = json.Unmarshal([]byte(data), &m)
		switch m.Role {
		case "user":
			rep.UserMessages++
		case "assistant":
			rep.AssistantMessages++
		}
		if m.Agent != "" {
			au := agentMap[m.Agent]
			if au == nil {
				au = &AgentUsage{Name: m.Agent}
				agentMap[m.Agent] = au
			}
			au.Messages++
			au.Tokens += tokenizer.Estimate(data)
		}
	}
	for _, au := range agentMap {
		rep.Agents = append(rep.Agents, *au)
	}
	return rows.Err()
}

func analyzeParts(parts []partRow, rep *Report) {
	toolMap := map[string]*ToolUsage{}
	skillMap := map[string]*SkillUsage{}

	for _, p := range parts {
		var raw struct {
			Type string `json:"type"`
			Tool string `json:"tool"`
			Text string `json:"text"`
			State struct {
				Status string `json:"status"`
				Input  json.RawMessage `json:"input"`
				Output string `json:"output"`
			} `json:"state"`
		}
		if err := json.Unmarshal([]byte(p.Data), &raw); err != nil {
			continue
		}
		switch raw.Type {
		case "tool":
			rep.TotalToolCalls++
			tu := toolMap[raw.Tool]
			if tu == nil {
				tu = &ToolUsage{Name: raw.Tool, Status: map[string]int{}}
				toolMap[raw.Tool] = tu
			}
			tu.Calls++
			tu.Status[raw.State.Status]++
			inTok := tokenizer.Estimate(string(raw.State.Input))
			outTok := tokenizer.Estimate(raw.State.Output)
			tu.InputTokens += inTok
			tu.OutputTokens += outTok

			// Detect skill loads
			if raw.Tool == "skill" {
				var input struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(raw.State.Input, &input)
				if input.Name != "" {
					su := skillMap[input.Name]
					if su == nil {
						su = &SkillUsage{Name: input.Name}
						skillMap[input.Name] = su
					}
					su.Loads++
					su.Tokens += inTok + outTok
				}
			}
		case "text":
			rep.TextParts++
		case "reasoning":
			rep.ReasoningParts++
		case "file":
			rep.FileParts++
		case "patch":
			rep.PatchParts++
		}
	}

	for _, tu := range toolMap {
		rep.Tools = append(rep.Tools, *tu)
	}
	for _, su := range skillMap {
		rep.Skills = append(rep.Skills, *su)
	}
}

func sortTools(tools []ToolUsage) {
	sort.Slice(tools, func(i, j int) bool {
		ti := tools[i].InputTokens + tools[i].OutputTokens
		tj := tools[j].InputTokens + tools[j].OutputTokens
		if ti != tj {
			return ti > tj
		}
		return tools[i].Calls > tools[j].Calls
	})
}

// builtinToolTokens holds tuned estimates (in tokens) for the JSON schema of
// opencode's built-in tools (v1.18.x). Each value approximates the size of
// the tool's name + description + parameter schema as sent to the provider
// at session startup.
var builtinToolTokens = map[string]int{
	"bash":      650,
	"compress":  800,
	"edit":      380,
	"glob":      200,
	"grep":      240,
	"question":  280,
	"read":      340,
	"skill":     110,
	"task":      750,
	"todowrite": 850,
	"webfetch":  220,
	"write":     180,
}

func estimateStartup(rep *Report, src Sources) {
	// System prompt baseline (heuristic).
	sp := src.SystemPromptTokens
	if sp <= 0 {
		sp = 2500
	}
	rep.Startup.SystemPromptTokens = sp
	rep.Startup.SystemPrompt = []StartupItem{{
		Name:             "System prompt",
		Description:      "Fixed opencode system prompt baseline (heuristic)",
		DescriptionTokens: sp,
		ContentTokens:    sp,
		LoadedTokens:     sp,
	}}

	// AGENTS.md content.
	rep.Startup.AGENTSMDTokens = 0
	if src.AGENTSMDPath != "" {
		if data, err := os.ReadFile(src.AGENTSMDPath); err == nil {
			content := string(data)
			rep.Startup.AGENTSMDTokens = tokenizer.Estimate(content)
			rep.Startup.AGENTSMD = []StartupItem{{
				Name:             filepath.Base(src.AGENTSMDPath),
				Description:      "Project/global instructions loaded at startup",
				DescriptionTokens: tokenizer.Estimate(extractDescription(content)),
				ContentTokens:    rep.Startup.AGENTSMDTokens,
				LoadedTokens:     rep.Startup.AGENTSMDTokens,
			}}
		}
	}

	// Built-in tool definitions: tuned estimates for the full set of
	// opencode core tools (their schemas are sent at every startup).
	rep.Startup.ToolTokens = 0
	names := make([]string, 0, len(builtinToolTokens))
	for name := range builtinToolTokens {
		names = append(names, name)
	}
	sort.Strings(names)
	rep.Startup.Tools = make([]StartupItem, 0, len(names))
	for _, name := range names {
		toks := builtinToolTokens[name]
		rep.Startup.Tools = append(rep.Startup.Tools, StartupItem{
			Name:              name,
			Description:       "Built-in tool schema (tuned estimate)",
			DescriptionTokens: toks,
			ContentTokens:     toks,
			LoadedTokens:      toks,
		})
		rep.Startup.ToolTokens += toks
	}
	sort.Slice(rep.Startup.Tools, func(i, j int) bool {
		if rep.Startup.Tools[i].LoadedTokens != rep.Startup.Tools[j].LoadedTokens {
			return rep.Startup.Tools[i].LoadedTokens > rep.Startup.Tools[j].LoadedTokens
		}
		return rep.Startup.Tools[i].Name < rep.Startup.Tools[j].Name
	})

	// Skill descriptions: read each SKILL.md in the skill dirs. At startup only
	// the frontmatter description is loaded into context, not the full file.
	rep.Startup.SkillTokens = 0
	skillFiles := collectSkillFiles(src.SkillDirs)
	rep.Startup.Skills = make([]StartupItem, 0, len(skillFiles))
	for _, f := range skillFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(data)
		contentTokens := tokenizer.Estimate(content)
		desc := extractDescription(content)
		descTokens := tokenizer.Estimate(desc)
		rep.Startup.Skills = append(rep.Startup.Skills, StartupItem{
			Name:              skillName(f),
			Description:       desc,
			DescriptionTokens: descTokens,
			ContentTokens:     contentTokens,
			LoadedTokens:      descTokens,
		})
		rep.Startup.SkillTokens += descTokens
	}
	sort.Slice(rep.Startup.Skills, func(i, j int) bool {
		if rep.Startup.Skills[i].DescriptionTokens != rep.Startup.Skills[j].DescriptionTokens {
			return rep.Startup.Skills[i].DescriptionTokens > rep.Startup.Skills[j].DescriptionTokens
		}
		return rep.Startup.Skills[i].Name < rep.Startup.Skills[j].Name
	})

	// Agent definitions: read the agent .md files if a dir is configured,
	// otherwise fall back to a flat estimate per agent used in the session.
	rep.Startup.AgentTokens = 0
	if src.AgentsDir != "" {
		rep.Startup.Agents = loadAgentItems(src.AgentsDir)
		for _, a := range rep.Startup.Agents {
			rep.Startup.AgentTokens += a.LoadedTokens
		}
	} else {
		rep.Startup.Agents = make([]StartupItem, 0, len(rep.Agents))
		for _, a := range rep.Agents {
			rep.Startup.Agents = append(rep.Startup.Agents, StartupItem{
				Name:              a.Name,
				Description:       "Agent definition (flat estimate)",
				DescriptionTokens: 250,
				ContentTokens:     250,
				LoadedTokens:      250,
			})
			rep.Startup.AgentTokens += 250
		}
	}

	// MCP tool definitions: live discovery via the MCP protocol.
	rep.Startup.MCPTokens = 0
	rep.Startup.MCP = loadMCPItems(src)
	for _, item := range rep.Startup.MCP {
		rep.Startup.MCPTokens += item.LoadedTokens
	}

	rep.Startup.Total = rep.Startup.SystemPromptTokens +
		rep.Startup.AGENTSMDTokens +
		rep.Startup.ToolTokens +
		rep.Startup.SkillTokens +
		rep.Startup.AgentTokens +
		rep.Startup.MCPTokens

	// Compute per-item percentages of the total startup context.
	computePercents(&rep.Startup)
}

// computePercents fills the Percent field of every startup item.
func computePercents(sc *StartupContext) {
	total := sc.Total
	if total <= 0 {
		return
	}
	apply := func(items []StartupItem) {
		for i := range items {
			items[i].Percent = float64(items[i].LoadedTokens) / float64(total) * 100
		}
	}
	apply(sc.SystemPrompt)
	apply(sc.AGENTSMD)
	apply(sc.Skills)
	apply(sc.Tools)
	apply(sc.Agents)
	apply(sc.MCP)
}

// extractDescription pulls the `description:` value from a SKILL.md frontmatter.
// It handles both inline values and YAML block scalars (`|` and `>`).
func extractDescription(content string) string {
	lines := strings.Split(content, "\n")
	inFront := false
	blockStyle := byte(0)
	var block []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "---" {
			if !inFront {
				inFront = true
				continue
			}
			break
		}
		if !inFront {
			continue
		}
		if blockStyle != 0 {
			// Collect indented block lines until dedent.
			if trimmed == "" || strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
				block = append(block, trimmed)
				continue
			}
			break
		}
		if strings.HasPrefix(trimmed, "description:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			if val == "|" || val == ">" || val == "|-" || val == ">-" {
				blockStyle = val[0]
				continue
			}
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	if blockStyle != 0 && len(block) > 0 {
		sep := " "
		if blockStyle == '|' {
			sep = "\n"
		}
		return strings.Join(block, sep)
	}
	return ""
}

// skillName derives a display name from a SKILL.md path.
func skillName(path string) string {
	dir := filepath.Dir(path)
	return filepath.Base(dir)
}

func collectSkillFiles(dirs []string) []string {
	var files []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			md := filepath.Join(dir, e.Name(), "SKILL.md")
			if _, err := os.Stat(md); err == nil {
				files = append(files, md)
			}
		}
	}
	return files
}

// loadAgentItems reads agent definition .md files and estimates their token
// cost. The full definition (frontmatter + body) is what opencode loads.
func loadAgentItems(dir string) []StartupItem {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []StartupItem{}
	}
	var items []StartupItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		toks := tokenizer.Estimate(content)
		desc := extractDescription(content)
		items = append(items, StartupItem{
			Name:              strings.TrimSuffix(e.Name(), ".md"),
			Description:       desc,
			DescriptionTokens: tokenizer.Estimate(desc),
			ContentTokens:     toks,
			LoadedTokens:      toks,
		})
	}
	// Agent definitions are loaded in full at startup, so sort by total size.
	sort.Slice(items, func(i, j int) bool {
		if items[i].LoadedTokens != items[j].LoadedTokens {
			return items[i].LoadedTokens > items[j].LoadedTokens
		}
		return items[i].Name < items[j].Name
	})
	return items
}

// loadMCPItems discovers MCP servers from the opencode config and fetches
// their tool lists live. Each tool's compact JSON definition is tokenized.
func loadMCPItems(src Sources) []StartupItem {
	items := []StartupItem{}
	if src.ConfigPath == "" {
		return items
	}
	servers, err := mcp.LoadServers(src.ConfigPath)
	if err != nil {
		return []StartupItem{{
			Name:        "(mcp config)",
			Description: "config error: " + shortText(err.Error()),
			LoadedTokens: 0,
		}}
	}
	timeout := src.MCPTimeout
	if timeout <= 0 {
		timeout = mcp.DefaultTimeout()
	}
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		tools, err := s.ListTools(ctx)
		cancel()
		if err != nil {
			items = append(items, StartupItem{
				Name:         s.Name,
				Description:  "discovery failed: " + shortText(err.Error()),
				LoadedTokens: 0,
			})
			continue
		}
		for _, t := range tools {
			toks := tokenizer.Estimate(t.JSONDefinition())
			items = append(items, StartupItem{
				Name:              s.Name + "_" + t.Name,
				Description:       shortText(t.Description),
				DescriptionTokens: tokenizer.Estimate(t.Description),
				ContentTokens:     toks,
				LoadedTokens:      toks,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DescriptionTokens != items[j].DescriptionTokens {
			return items[i].DescriptionTokens > items[j].DescriptionTokens
		}
		return items[i].Name < items[j].Name
	})
	return items
}

// shortText collapses whitespace and truncates for table cells.
func shortText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// ToolNames returns the sorted list of distinct tool names.
func (r *Report) ToolNames() []string {
	names := make([]string, 0, len(r.Tools))
	for _, t := range r.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// SkillNames returns the sorted list of distinct skill names.
func (r *Report) SkillNames() []string {
	names := make([]string, 0, len(r.Skills))
	for _, s := range r.Skills {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// AgentNames returns the sorted list of distinct agent names.
func (r *Report) AgentNames() []string {
	names := make([]string, 0, len(r.Agents))
	for _, s := range r.Agents {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}
