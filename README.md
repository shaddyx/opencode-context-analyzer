# opencode-context-analyzer

A terminal UI (TUI) that inspects the opencode database, lists your sessions,
and produces a comprehensive context-usage report grouped by tools, skills,
agents, and startup context. Reports can be exported to Markdown.

Repository: <https://github.com/shaddyx/opencode-context-analyzer>

## Features

- **Session list** — browse all sessions from the opencode SQLite database with
  model, message/tool counts, and token usage.
- **Context analysis** — for a selected session, computes token usage per tool,
  skill, and agent, plus the startup context loaded at session start
  (system prompt, AGENTS.md, skills, tools, agents, MCP).
- **Styled preview** — the report is rendered as Markdown in the terminal via
  glamour.
- **Export to Markdown** — write the full report to a `.md` file.

## Install

Requires Go 1.21+.

```sh
go install github.com/shaddyx/opencode-context-analyzer@latest
```

Or build from source:

```sh
git clone https://github.com/shaddyx/opencode-context-analyzer.git
cd opencode-context-analyzer
go build -o opencode-context-analyzer .
```

## Usage

```sh
./opencode-context-analyzer [flags]
```

### Flags

| Flag       | Default                                        | Description                              |
| ---------- | ---------------------------------------------- | ---------------------------------------- |
| `-db`      | `~/.local/share/opencode/opencode.db`          | Path to the opencode SQLite database.     |
| `-agents`  | `~/.config/opencode/AGENTS.md`                 | Path to AGENTS.md for the startup estimate. |
| `-skills`  | `~/.agents/skills`, `~/.config/opencode/skills` | Directory(ies) containing `SKILL.md` subfolders. |

The database path can also be set with the `OPENCODE_DB` environment variable.

### Keys

| Key            | Action                          |
| -------------- | ------------------------------- |
| `enter`        | Analyze the selected session.  |
| `e`            | Export the report to Markdown.  |
| `esc` / `backspace` | Back to the session list.   |
| `q` / `ctrl+c`  | Quit.                          |

## Report contents

- **Summary** — totals for tokens, tool calls, skills, and agents.
- **Startup Context** — tokens loaded at session start, broken down by system
  prompt, AGENTS.md, skills, tools, agents, and MCP, with per-item subtables.
- **Tools** — calls, input/output tokens, and status per tool.
- **Skills** — loads and tokens per skill.
- **Agents** — messages and tokens per agent.
- **Conversation Breakdown** — text, reasoning, file, and patch part counts.

## Project layout

```
main.go                 CLI entry point, flag parsing, source resolution
internal/db/            Read-only SQLite access and session listing
internal/analyzer/      Context analysis (tools, skills, agents, startup)
internal/report/        Markdown report generation
internal/tokenizer/     Heuristic token estimation
internal/tui/           Bubble Tea terminal UI
```

## Notes

- The database is opened read-only.
- Token counts are estimates produced by a heuristic tokenizer, not the exact
  counts reported by the model provider.
