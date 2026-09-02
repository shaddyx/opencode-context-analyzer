// Package report renders an analyzer.Report as a Markdown document.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"opencode-context-analyzer/internal/analyzer"
	"opencode-context-analyzer/internal/tokenizer"
)

func statusSummary(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// Render produces a Markdown report for the given analysis.
func Render(rep *analyzer.Report) string {
	var b strings.Builder

	b.WriteString("# Context Usage Report\n\n")
	b.WriteString(fmt.Sprintf("- **Session:** %s\n", rep.SessionTitle))
	b.WriteString(fmt.Sprintf("- **Session ID:** `%s`\n", rep.SessionID))
	b.WriteString(fmt.Sprintf("- **Directory:** `%s`\n", rep.Directory))
	b.WriteString(fmt.Sprintf("- **Model:** `%s`\n", rep.Model))
	b.WriteString(fmt.Sprintf("- **Agent:** `%s`\n", rep.Agent))
	b.WriteString(fmt.Sprintf("- **Generated:** %s\n\n", time.Now().Format(time.RFC3339)))

	// Summary
	b.WriteString("## Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| --- | --- |\n")
	b.WriteString(fmt.Sprintf("| Total estimated tokens | %s |\n", tokenizer.Format(rep.TotalTokens)))
	b.WriteString(fmt.Sprintf("| Total messages | %d |\n", rep.TotalMessages))
	b.WriteString(fmt.Sprintf("| User messages | %d |\n", rep.UserMessages))
	b.WriteString(fmt.Sprintf("| Assistant messages | %d |\n", rep.AssistantMessages))
	b.WriteString(fmt.Sprintf("| Tool calls | %d |\n", rep.TotalToolCalls))
	b.WriteString(fmt.Sprintf("| Distinct tools | %d |\n", len(rep.Tools)))
	b.WriteString(fmt.Sprintf("| Skills loaded | %d |\n", len(rep.Skills)))
	b.WriteString(fmt.Sprintf("| Agents used | %d |\n", len(rep.Agents)))
	b.WriteString("\n")

	// Startup context
	b.WriteString("## Startup Context (loaded at session start)\n\n")
	b.WriteString("| Component | Tokens |\n")
	b.WriteString("| --- | --- |\n")
	b.WriteString(fmt.Sprintf("| System prompt | %s |\n", tokenizer.Format(rep.Startup.SystemPromptTokens)))
	b.WriteString(fmt.Sprintf("| AGENTS.md | %s |\n", tokenizer.Format(rep.Startup.AGENTSMDTokens)))
	b.WriteString(fmt.Sprintf("| Skill descriptions | %s |\n", tokenizer.Format(rep.Startup.SkillTokens)))
	b.WriteString(fmt.Sprintf("| Tool definitions | %s |\n", tokenizer.Format(rep.Startup.ToolTokens)))
	b.WriteString(fmt.Sprintf("| Agent definitions | %s |\n", tokenizer.Format(rep.Startup.AgentTokens)))
	b.WriteString(fmt.Sprintf("| MCP tool descriptions | %s |\n", tokenizer.Format(rep.Startup.MCPTokens)))
	b.WriteString(fmt.Sprintf("| **Total startup** | **%s** |\n", tokenizer.Format(rep.Startup.Total)))
	b.WriteString("\n")

	// Tools
	b.WriteString("## Tools\n\n")
	if len(rep.Tools) == 0 {
		b.WriteString("_No tool calls recorded._\n\n")
	} else {
		b.WriteString("| Tool | Calls | Input tokens | Output tokens | Total tokens | Status |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, t := range rep.Tools {
			total := t.InputTokens + t.OutputTokens
			status := statusSummary(t.Status)
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s | %s | %s |\n",
				t.Name, t.Calls,
				tokenizer.Format(t.InputTokens),
				tokenizer.Format(t.OutputTokens),
				tokenizer.Format(total),
				status))
		}
	}
	b.WriteString("\n")

	// Skills
	b.WriteString("## Skills\n\n")
	if len(rep.Skills) == 0 {
		b.WriteString("_No skills loaded._\n\n")
	} else {
		b.WriteString("| Skill | Loads | Tokens |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, s := range rep.Skills {
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s |\n", s.Name, s.Loads, tokenizer.Format(s.Tokens)))
		}
	}
	b.WriteString("\n")

	// Agents
	b.WriteString("## Agents\n\n")
	if len(rep.Agents) == 0 {
		b.WriteString("_No agents recorded._\n\n")
	} else {
		b.WriteString("| Agent | Messages | Tokens |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, a := range rep.Agents {
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s |\n", a.Name, a.Messages, tokenizer.Format(a.Tokens)))
		}
	}
	b.WriteString("\n")

	// Conversation breakdown
	b.WriteString("## Conversation Breakdown\n\n")
	b.WriteString("| Part type | Count |\n")
	b.WriteString("| --- | --- |\n")
	b.WriteString(fmt.Sprintf("| Text | %d |\n", rep.TextParts))
	b.WriteString(fmt.Sprintf("| Reasoning | %d |\n", rep.ReasoningParts))
	b.WriteString(fmt.Sprintf("| File | %d |\n", rep.FileParts))
	b.WriteString(fmt.Sprintf("| Patch | %d |\n", rep.PatchParts))
	b.WriteString("\n")

	return b.String()
}
