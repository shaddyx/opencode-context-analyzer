// Package report renders an analyzer.Report as a Markdown document.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shaddyx/opencode-context-analyzer/internal/analyzer"
	"github.com/shaddyx/opencode-context-analyzer/internal/tokenizer"
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

// pct returns the percentage of part within total (0 if total is 0).
func pct(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// renderStartupSubtable renders a per-category subtable of startup items.
// When every item's ContentTokens equals LoadedTokens (e.g. tools, agents),
// the two columns are collapsed into a single "Tokens" column.
func renderStartupSubtable(b *strings.Builder, title string, items []analyzer.StartupItem) {
	if len(items) == 0 {
		return
	}
	distinct := false
	for _, it := range items {
		if it.ContentTokens != it.LoadedTokens {
			distinct = true
			break
		}
	}
	b.WriteString("### " + title + "\n\n")
	var sumDesc, sumContent, sumLoaded int
	var sumPercent float64
	if distinct {
		b.WriteString("| Name | Description | Description tokens | Content tokens | Loaded tokens | % of total |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, it := range items {
			desc := it.Description
			if desc == "" {
				desc = "-"
			}
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s | %.1f%% |\n",
				it.Name,
				mdEscape(desc),
				tokenizer.Format(it.DescriptionTokens),
				tokenizer.Format(it.ContentTokens),
				tokenizer.Format(it.LoadedTokens),
				it.Percent))
			sumDesc += it.DescriptionTokens
			sumContent += it.ContentTokens
			sumLoaded += it.LoadedTokens
			sumPercent += it.Percent
		}
		b.WriteString(fmt.Sprintf("| **Total** | | **%s** | **%s** | **%s** | **%.1f%%** |\n",
			tokenizer.Format(sumDesc),
			tokenizer.Format(sumContent),
			tokenizer.Format(sumLoaded),
			sumPercent))
	} else {
		b.WriteString("| Name | Description | Description tokens | Tokens | % of total |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, it := range items {
			desc := it.Description
			if desc == "" {
				desc = "-"
			}
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %.1f%% |\n",
				it.Name,
				mdEscape(desc),
				tokenizer.Format(it.DescriptionTokens),
				tokenizer.Format(it.LoadedTokens),
				it.Percent))
			sumDesc += it.DescriptionTokens
			sumLoaded += it.LoadedTokens
			sumPercent += it.Percent
		}
		b.WriteString(fmt.Sprintf("| **Total** | | **%s** | **%s** | **%.1f%%** |\n",
			tokenizer.Format(sumDesc),
			tokenizer.Format(sumLoaded),
			sumPercent))
	}
	b.WriteString("\n")
}

// mdEscape escapes pipe characters in table cell text.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
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
	b.WriteString(fmt.Sprintf("| Message tokens | %s |\n", tokenizer.Format(rep.MessageTokens)))
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
	b.WriteString("| Component | Tokens | % of total |\n")
	b.WriteString("| --- | --- | --- |\n")
	b.WriteString(fmt.Sprintf("| System prompt | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.SystemPromptTokens), pct(rep.Startup.SystemPromptTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| AGENTS.md | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.AGENTSMDTokens), pct(rep.Startup.AGENTSMDTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| Skill descriptions | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.SkillTokens), pct(rep.Startup.SkillTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| Tool definitions | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.ToolTokens), pct(rep.Startup.ToolTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| Agent definitions | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.AgentTokens), pct(rep.Startup.AgentTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| MCP tool descriptions | %s | %.1f%% |\n",
		tokenizer.Format(rep.Startup.MCPTokens), pct(rep.Startup.MCPTokens, rep.Startup.Total)))
	b.WriteString(fmt.Sprintf("| **Total startup** | **%s** | **100.0%%** |\n", tokenizer.Format(rep.Startup.Total)))
	b.WriteString("\n")

	// Per-category subtables
	renderStartupSubtable(&b, "System Prompt", rep.Startup.SystemPrompt)
	renderStartupSubtable(&b, "AGENTS.md", rep.Startup.AGENTSMD)
	renderStartupSubtable(&b, "Skills", rep.Startup.Skills)
	renderStartupSubtable(&b, "Tools", rep.Startup.Tools)
	renderStartupSubtable(&b, "Agents", rep.Startup.Agents)
	renderStartupSubtable(&b, "MCP", rep.Startup.MCP)

	// Compare the estimate against the provider-reported startup size.
	if rep.RealStartupTokens > 0 {
		delta := rep.RealStartupTokens - rep.Startup.Total
		b.WriteString("## Startup Accuracy\n\n")
		b.WriteString("| Metric | Value |\n")
		b.WriteString("| --- | --- |\n")
		b.WriteString(fmt.Sprintf("| Provider-reported (first-turn input) | %s |\n", tokenizer.Format(rep.RealStartupTokens)))
		b.WriteString(fmt.Sprintf("| Estimated startup total | %s |\n", tokenizer.Format(rep.Startup.Total)))
		b.WriteString(fmt.Sprintf("| Residual (delta) | %s |\n", tokenizer.Format(delta)))
		b.WriteString("\n")
		b.WriteString("The residual covers pieces the estimate cannot reconstruct: the exact system-prompt " +
			"text, plugin-provided tools, the first user message, and tokenizer differences.\n\n")
	}

	// Delimiter marking the end of the startup context and the start of the
	// session's own content.
	b.WriteString("---\n\n")
	b.WriteString("## Session Content\n\n")

	// Tools
	b.WriteString("## Tools\n\n")
	if len(rep.Tools) == 0 {
		b.WriteString("_No tool calls recorded._\n\n")
	} else {
		b.WriteString("| Tool | Calls | Input tokens | Output tokens | Total tokens | Status |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		var sumCalls, sumInput, sumOutput int
		for _, t := range rep.Tools {
			total := t.InputTokens + t.OutputTokens
			status := statusSummary(t.Status)
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s | %s | %s | %s |\n",
				t.Name, t.Calls,
				tokenizer.Format(t.InputTokens),
				tokenizer.Format(t.OutputTokens),
				tokenizer.Format(total),
				status))
			sumCalls += t.Calls
			sumInput += t.InputTokens
			sumOutput += t.OutputTokens
		}
		b.WriteString(fmt.Sprintf("| **Total** | **%d** | **%s** | **%s** | **%s** | |\n",
			sumCalls,
			tokenizer.Format(sumInput),
			tokenizer.Format(sumOutput),
			tokenizer.Format(sumInput+sumOutput)))
	}
	b.WriteString("\n")

	// Skills
	b.WriteString("## Skills\n\n")
	if len(rep.Skills) == 0 {
		b.WriteString("_No skills loaded._\n\n")
	} else {
		b.WriteString("| Skill | Loads | Tokens |\n")
		b.WriteString("| --- | --- | --- |\n")
		var sumLoads, sumTokens int
		for _, s := range rep.Skills {
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s |\n", s.Name, s.Loads, tokenizer.Format(s.Tokens)))
			sumLoads += s.Loads
			sumTokens += s.Tokens
		}
		b.WriteString(fmt.Sprintf("| **Total** | **%d** | **%s** |\n", sumLoads, tokenizer.Format(sumTokens)))
	}
	b.WriteString("\n")

	// Agents
	b.WriteString("## Agents\n\n")
	if len(rep.Agents) == 0 {
		b.WriteString("_No agents recorded._\n\n")
	} else {
		b.WriteString("| Agent | Messages | Tokens |\n")
		b.WriteString("| --- | --- | --- |\n")
		var sumMessages, sumTokens int
		for _, a := range rep.Agents {
			b.WriteString(fmt.Sprintf("| `%s` | %d | %s |\n", a.Name, a.Messages, tokenizer.Format(a.Tokens)))
			sumMessages += a.Messages
			sumTokens += a.Tokens
		}
		b.WriteString(fmt.Sprintf("| **Total** | **%d** | **%s** |\n", sumMessages, tokenizer.Format(sumTokens)))
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
