// Package analyzer inspects a session's parts and messages to build a
// comprehensive context-usage report grouped by tools, skills, agents, etc.
package analyzer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"opencode-context-analyzer/internal/tokenizer"
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
	// ToolTokensPerTool is the estimated tokens per tool definition.
	ToolTokensPerTool int
	// AgentTokensPerAgent is the estimated tokens per agent definition.
	AgentTokensPerAgent int
	// SystemPromptTokens is the fixed system prompt baseline.
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

func estimateStartup(rep *Report, src Sources) {
	// System prompt is a fixed baseline.
	sp := src.SystemPromptTokens
	if sp <= 0 {
		sp = 1200
	}
	rep.Startup.SystemPromptTokens = sp
	rep.Startup.SystemPrompt = []StartupItem{{
		Name:             "System prompt",
		Description:      "Fixed opencode system prompt baseline",
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

	// Tool definitions: each distinct tool contributes a description.
	perTool := src.ToolTokensPerTool
	if perTool <= 0 {
		perTool = 150
	}
	rep.Startup.ToolTokens = 0
	rep.Startup.Tools = make([]StartupItem, 0, len(rep.Tools))
	for _, t := range rep.Tools {
		rep.Startup.Tools = append(rep.Startup.Tools, StartupItem{
			Name:             t.Name,
			Description:      "Tool definition injected into context",
			DescriptionTokens: perTool,
			ContentTokens:    perTool,
			LoadedTokens:     perTool,
		})
		rep.Startup.ToolTokens += perTool
	}

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

	// Agent definitions.
	perAgent := src.AgentTokensPerAgent
	if perAgent <= 0 {
		perAgent = 250
	}
	rep.Startup.AgentTokens = 0
	rep.Startup.Agents = make([]StartupItem, 0, len(rep.Agents))
	for _, a := range rep.Agents {
		rep.Startup.Agents = append(rep.Startup.Agents, StartupItem{
			Name:             a.Name,
			Description:      "Agent definition injected into context",
			DescriptionTokens: perAgent,
			ContentTokens:    perAgent,
			LoadedTokens:     perAgent,
		})
		rep.Startup.AgentTokens += perAgent
	}

	// MCP tool descriptions.
	rep.Startup.MCPTokens = 0
	rep.Startup.MCP = []StartupItem{}

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

// String is a helper for building detail strings.
func joinNames(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}