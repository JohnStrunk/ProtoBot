# ProtoBot: Claude Code Harness Binding

> Design document — September 2026
>
> The second harness binding of the
> [Agent Harness Adapter Contract](adapter-contract.md): the Claude Code
> files, how they are meant to meet each obligation, and the documented
> behaviors they rely on. Designed against Claude Code 2.1.273; the
> fixture has not run on it.

**Contents:**

- [Purpose and scope](#purpose-and-scope)
- [Binding files](#binding-files)
- [Skill discovery and invocation](#skill-discovery-and-invocation)
- [Obligation status](#obligation-status)
- [Documented Claude Code behaviors](#documented-claude-code-behaviors)
- [Running the fixture on Claude Code](#running-the-fixture-on-claude-code)
- [Open points](#open-points)
- [Related Documents](#related-documents)

---

## Purpose and scope

This document binds the
[Agent Harness Adapter Contract](adapter-contract.md) to Claude Code, so
the Drafting Table runs there with the same Toolkit, the same guard, and
the same fixture as in OpenCode. It adds no rule of its own. Where
Claude Code forces a choice, this document states the choice and the
obligation it serves.

Unlike the [OpenCode binding](opencode.md), the fixture has not run
here yet. The skill and settings-source behaviors in
[Skill visibility](#skill-visibility) were observed on 2026-09-16 in
headless test runs. Every other behavior comes from the Claude Code
documentation and the CLI help of version 2.1.273, read the same day. The
[fixture](#running-the-fixture-on-claude-code) turns each "designed"
status into "met" or into a recorded gap, and [Open points](#open-points)
lists what it must confirm first.

---

## Binding files

### Installed files

The binding is five files. At project scope they sit beside the shared
layer:

```text
<project root>/
├── .claude/
│   ├── settings.json                    hook and deny rules, every session
│   ├── agents/drafting-table.md         the Drafting Table role and entry point
│   ├── settings.drafting-table.json     the role's native rules, loaded at launch
│   ├── mcp.drafting-table.json          the wms MCP server, loaded at launch
│   └── hooks/drafting-table-guard.sh    the shim that names the role
└── .agents/                             shared layer (harness-neutral)
    ├── drafting-table.yaml
    └── skills/
```

At user scope the same files go under `~/.claude/`. Claude Code reads
`.claude/skills/`, not `.agents/skills/`; in the ProtoBot repository
`.claude/skills` is a link to `.agents/skills`, which is the link that
skill rule 1 of the contract allows.

The role is launched, not selected inside a session:

```text
claude --agent drafting-table \
  --setting-sources project \
  --settings .claude/settings.drafting-table.json \
  --mcp-config .claude/mcp.drafting-table.json --strict-mcp-config \
  [--continue | --resume <id>] "<intent>"
```

Claude Code has no documented way to switch a running session to
another agent. A user who is already in a session exits and relaunches
with `--continue`: the conversation continues, and the role changes.

`--setting-sources project` keeps user and local settings out of the
role's launch. Allow rules from every loaded settings file add up, and
only a deny cannot be undone, so a user-scope `Bash` or `Skill` allow
would let every shell command or skill pass the role's native rules.
Project settings still load, so the guard hook runs, and the role's
settings file still loads through `--settings`. The cost is that a
user-scope model, environment, or hook setting does not reach the role
either.

### `.claude/settings.json`

Project settings apply to every session in the project, whatever its
agent:

```json
{
  "permissions": {
    "deny": [
      "Edit(/.protobot/**)",
      "Write(/.protobot/**)",
      "NotebookEdit(/.protobot/**)"
    ]
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/drafting-table-guard.sh"
          }
        ]
      }
    ]
  }
}
```

- **The deny rules** are a static copy of guard rule 3 for
  `.protobot/`. A deny rule wins over every allow rule in every scope,
  so it binds the Drafting Table role too, which is correct: the role
  never writes there either.
- **The hook** calls the guard before every tool call of every session
  (H8). The matcher covers MCP tools as well.
- The file holds no allow rule, so it changes nothing else for other
  sessions.

### The `drafting-table` agent

`.claude/agents/drafting-table.md` gives the Drafting Table role to one
custom agent, and launching it is the entry point (H3):

```markdown
---
name: drafting-table
description: ProtoBot Drafting Table. Launch with --agent; never delegate to it.
tools: Read, Grep, Glob, Bash, Skill, TodoWrite, AskUserQuestion
permissionMode: dontAsk
skills:
  - drafting-specifications
---

You are the ProtoBot Drafting Table, running in Claude Code with the
Claude Code binding of adapter layout 1. Begin every start summary
with the line "Adapter: ProtoBot adapter layout 1, Claude Code binding."

Follow the drafting-specifications skill.

Toolkit skills name operations. In Claude Code:
- an `ears-manager` operation is one shell command with
  `--output json`, and its long text goes on standard input;
- a WMS operation is the tool `mcp__wms__<operation>`; and
- a Git or Git host operation is one shell command.
```

- **`tools` is the role's tool set (H9).** `Edit`, `Write`,
  `NotebookEdit`, `Agent`, `WebFetch`, and `WebSearch` are absent.
  Whether an agent with a `tools` list still receives the MCP tools of
  `--mcp-config` is undocumented ([Open points](#open-points)); the
  role's settings file allows them either way.
- **`permissionMode: dontAsk` (H7).** Every call that would prompt is
  denied instead, so the role behaves the same headless and
  interactive. It does not restrict skills: a skill with only `name`
  and `description` needs no permission
  ([Skill visibility](#skill-visibility)).
- **`skills` preloads the session skill.** The role starts with
  `drafting-specifications` loaded, so no command file exists. The
  name `drafting-table` is an agent name, not a skill name, so it
  cannot collide with a Toolkit skill.
- **The prompt holds Claude Code facts only:** the binding and layout
  line, the skill to follow, and the tool-name mapping. The adapter
  line is spoken in the first turn, because the transcript stores the
  conversation, not the agent prompt
  ([Traces](adapter-contract.md#traces)).
- **The description says never to delegate to it.** Claude Code may
  otherwise pick a project agent for a subtask; the guard refuses a
  subagent inside the role in any case.

### The role's settings file

`.claude/settings.drafting-table.json` is loaded with `--settings` and
holds the native copy of the role's rules:

```json
{
  "env": { "PROTOBOT_ROLE": "drafting-table" },
  "permissions": {
    "allow": [
      "Read", "Grep", "Glob", "TodoWrite", "AskUserQuestion",
      "Skill(drafting-specifications)", "Skill(eliciting-requirements)",
      "mcp__wms", "Bash(ears-manager *)",
      "Bash(git rev-parse --show-toplevel)",
      "Bash(git rev-parse --abbrev-ref HEAD)",
      "Bash(git rev-parse --verify *)", "Bash(git status --porcelain)",
      "Bash(git merge-base *)", "Bash(git remote -v)",
      "Bash(git fetch origin)",
      "Bash(git switch -c cs/*)", "Bash(git switch cs/*)",
      "Bash(git add -- *)", "Bash(git commit -F - *)",
      "Bash(git merge --no-ff --no-edit origin/main)",
      "Bash(git merge --abort)", "Bash(git push origin cs/*)",
      "Bash(gh pr create --repo *)", "Bash(gh pr edit cs/*)",
      "Bash(gh pr view cs/*)",
      "Bash(register-approved-change-set *)"
    ],
    "deny": [
      "Edit", "Write", "NotebookEdit", "Agent", "WebFetch", "WebSearch",
      "Read(/.env)", "Read(/.env.*)", "Read(/.protobot/**)"
    ]
  },
  "skillOverrides": {
    "board-tidy": "off", "issue-audit": "off", "issue-sync": "off",
    "pull-request": "off", "rebase-pr": "off", "review-pr": "off",
    "batch": "off", "claude-api": "off", "code-review": "off",
    "dataviz": "off", "debug": "off", "deep-research": "off",
    "design": "off", "design-sync": "off", "doctor": "off",
    "fewer-permission-prompts": "off", "init": "off",
    "keybindings-help": "off", "loop": "off", "run": "off",
    "run-skill-generator": "off", "schedule": "off",
    "security-review": "off", "simplify": "off", "update-config": "off",
    "verify": "off", "workflow-authoring": "off"
  }
}
```

- **`env.PROTOBOT_ROLE` is the role signal.** Settings apply
  environment variables to the session, and a hook command inherits
  them, so the shim names the role without an undocumented hook field.
- **The allow list is the native copy of the
  [shell operations](adapter-contract.md#shell-operations)** with the
  defaults `origin`, `cs/`, and `main`, and `*` where the form has
  `<repo>`, `<branch>`, `<rev>`, or `<path>`. A rule without `*`
  matches only that exact command, so seven of the eighteen Git, `gh`,
  and registration rules leave no room for an extra option. A rule
  with a trailing `*` matches a command prefix and cannot express a
  forbidden option inside a command, such as `--force` after the
  branch, so those forms have no native deny and the guard refuses
  them. This early layer is weaker than OpenCode's, and H8 carries the
  difference.
- **The deny list hides the file-writing, subagent, and web tools
  (H9)** and copies the two read denies of the role table.
- **`skillOverrides` hides every skill the binding can name (H10).**
  The first six names are the project's maintenance skills in
  `.agents/skills/`; the rest are the Claude Code 2.1.273 built-in
  skills that reached the model in the test runs. A skill set to `off`
  is left out of the model's list and refused when called. The file is
  loaded only at the role's launch, so other sessions keep every skill.
  The two `Skill(<name>)` allow rules grant nothing that the Toolkit
  skills' plain frontmatter does not already allow; they name the
  Toolkit skills for a reader and for a later version that asks.
- Deny wins over allow in every scope, so a deny in any loaded settings
  file narrows the role. An allow in any loaded settings file widens
  it, which is why the launch loads no user or local settings.

### The `wms` MCP server

`.claude/mcp.drafting-table.json`:

```json
{
  "mcpServers": {
    "wms": { "command": "<WMS Adapter MCP server, named by #31>" }
  }
}
```

- The `wms` server is the manifest's only MCP server (H2). Claude Code
  names its tools `mcp__wms__<operation>`. `ears-manager` needs no
  entry: it is a shell operation, allowed by `Bash(ears-manager *)`.
- **It is loaded at launch, with `--strict-mcp-config`, and is not in
  `.mcp.json`.** A deny rule in project settings would bind the role
  too, so the `wms` tools cannot be denied for every session and
  allowed for the role, as OpenCode does. Instead the server exists
  only in the role's launch: other sessions never see it, and the role
  sees no other server. That is the MCP half of guard rule 4, in both
  directions.
- The entry holds no credential. In multi-player mode it becomes an
  HTTP server, and the user authenticates once through `/mcp` (H13,
  unverified).

### The guard shim

`.claude/hooks/drafting-table-guard.sh` passes the hook input through
and adds the two arguments the guard needs (H8):

```sh
#!/bin/sh
exec drafting-table-guard --harness claude-code \
  --role "${PROTOBOT_ROLE:-other}"
```

Claude Code sends the `PreToolUse` JSON on standard input, blocks the
call when the command exits 2, and shows the guard's line to the model.
Exit 0 lets Claude Code's own rules decide. The shim holds no rule,
reads no file, and registers no `Stop` or `SessionEnd` hook (H5).

---

## Skill discovery and invocation

### Discovery

Claude Code discovers skills from `.claude/skills/<name>/SKILL.md` in
the project, from `~/.claude/skills/` for the user, and from enabled
plugins (H1). In the ProtoBot repository `.claude/skills` links to
`.agents/skills/`, so every Toolkit skill is found through the link and
no copy exists. A project that installs the adapter adds the same link.

When one name exists in two locations, the documented order is
enterprise, then user, then project, so a user-scope skill hides a
project skill of the same name. That is one reason skill rule 3 of the
contract demands unique names across every discovery location.

### Skill visibility

Discovery is not permission, and in Claude Code permission does not
decide what the model sees. Claude Code sends every discovered skill to
the model and checks a permission rule only when the model calls one.
Headless runs on 2.1.273, on 2026-09-16, showed:

| Setting | Skill in the model's list | The model calls the skill |
| --- | --- | --- |
| `Skill(<other>)` allow rules only, with `dontAsk` | Listed | A skill with only `name` and `description` runs without any permission. A skill that declares `allowed-tools` is denied. |
| A `Skill(<name>)` deny rule | Listed | Refused: "Skill execution blocked by permission rules" |
| `skillOverrides` with `"<name>": "off"` | Left out, also from the init message | Refused: the skill "is disabled for model invocation" |
| `skillOverrides` with `"*": "off"` | No effect | No effect |
| `--disable-slash-commands` | No skill at all, Toolkit skills included | No `Skill` tool |

`skillOverrides` is therefore the only way to keep a skill out of the
model's list, and it takes exact names. The role's settings file turns
off every skill that the binding can name: the project's maintenance
skills, such as `pull-request` and `review-pr`, and the built-in skills
of the pinned Claude Code version. It works for built-in skills too.

A skill that the binding cannot name in advance stays in the list: one
in another user's `~/.claude/skills/`, or one from a plugin that user
enabled. The guard refuses a call to it (guard rule 5), so the gap is
what the model sees, not what it can load. H10 records that gap.

### Invocation

| Entry point | Effect |
| --- | --- |
| `claude --agent drafting-table ... "<intent>"` | A new session in the role |
| `claude --agent drafting-table ... --continue` | The most recent conversation in this directory continues in the role; the resume steps run again |
| `claude --agent drafting-table ... --resume <id>` | The named conversation continues in the role |
| `claude -p --agent drafting-table ... --output-format stream-json` | A headless session; the fixture uses it |

`...` stands for the `--setting-sources`, `--settings`, and
`--mcp-config` flags above. A
session without `--agent drafting-table` has no governed tools, and the
guard refuses them.

### The first consumer in Claude Code

`eliciting-requirements` loads through the `Skill` tool, or as
`/eliciting-requirements` typed by the user, and reads its `references/`
through `Read`. It needs no binding file.

---

## Obligation status

| # | Obligation | Claude Code binding | Status |
| --- | --- | --- | --- |
| H1 | Discover Toolkit skills from `.agents/skills/` | The `.claude/skills` link | Designed |
| H2 | The `ears-manager` CLI and the `wms` tools for the role | `Bash(ears-manager *)`; `--mcp-config` with `--strict-mcp-config` at launch | Designed |
| H3 | `drafting-table` entry point | The agent, launched with `--agent`; `skills` preloads the session skill | Designed |
| H4 | Resume on every entry, continued session, and compaction | `--continue` and `--resume` keep the conversation; the session skill runs the resume steps | Designed |
| H5 | Nothing on idle or exit | No `Stop` or `SessionEnd` hook | Designed |
| H6 | Replayable session record | The transcript under `~/.claude/projects/` and the `stream-json` output with `--include-hook-events` | Designed; no export or redaction command exists |
| H7 | Headless replay with no permission prompt | `-p`, `dontAsk`, `--permission-prompts none`, and `ANTHROPIC_BASE_URL` to a replay endpoint | Designed; the replay endpoint is unverified |
| H8 | Guard before every tool call | The project hook and the shim | Designed |
| H9 | Hide file-writing, subagent, and web tools | The agent's `tools` list and the deny rules | Designed |
| H10 | Toolkit skills only | `skillOverrides` turns off every skill the binding can name; the guard refuses a call to any other | Observed for project and built-in skills; a user-scope or plugin skill stays in the model's list (gap) |
| H11 | No credential in binding files; no session upload | Placeholders; no `--remote-control`, `--cloud`, or `/feedback`; `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` in the fixture | Designed |
| H12 | Publish this status | This table | Met |
| H13 | Remote `wms` server with OAuth 2.1 | An HTTP MCP server with OAuth through `/mcp` | Candidate; not checked |

"Designed" means the mechanism is documented and the fixture has not
run. Nothing else is marked met.

What the Claude Code layer stops, by route:

| Write route to a guarded path | `drafting-table` agent | Every other session |
| --- | --- | --- |
| File tool under `.protobot/` | Tool not offered; also denied by the project rule | Denied by the project rule and the guard |
| File tool on a registered path elsewhere | Tool not offered | Refused by the guard |
| Shell writer, such as `sed -i` | Denied by `dontAsk` with no allow rule, and refused by the guard | Not stopped |
| Output redirection in a shell command | Refused by the guard; a prefix rule cannot see it | Refused by the guard when the redirection target is written from the project root |
| Tool of another MCP server | Not loaded, because of `--strict-mcp-config` | The user's own configuration |
| Subagent | `Agent` not offered | Not applicable |

---

## Documented Claude Code behaviors

The binding relies on these behaviors. Each is documented for Claude
Code 2.1.273; none has been observed by the fixture. Items 2 and 11 were
also observed in headless test runs on 2026-09-16.

1. Skills are discovered from `.claude/skills/`, `~/.claude/skills/`,
   and plugins, and a user-scope name hides a project name.
2. Every discovered skill reaches the model's list whatever the
   `Skill(<name>)` rules say. A deny rule refuses the call. A skill with
   only `name` and `description` runs without permission even with
   `dontAsk`, which denies every other call that would prompt.
   `skillOverrides` with `off` removes a named skill from the list and
   refuses the call; `"*"` does nothing.
3. A custom agent runs as the main session with `--agent`, in
   interactive and print mode, and combines with `--continue` and
   `--resume`.
4. A deny rule wins over an allow rule in every scope, `--settings`
   loads an additional settings file, and `env` in settings applies to
   the session.
5. A `PreToolUse` command hook receives the call as JSON on standard
   input; exit status 2 blocks the call and shows standard error to the
   model. `SessionStart` reports `compact` as a source.
6. A Bash rule without `*` matches the exact command, and a trailing
   `*` matches a command prefix; Read and Edit rules take
   gitignore-style path patterns.
7. `-p --output-format stream-json` streams the init message, every
   assistant message, tool call, and tool result;
   `--include-hook-events` adds hook events; a call that would prompt
   is denied and the run continues.
8. The transcript is a JSONL file under `~/.claude/projects/`; there is
   no export or redaction command.
9. Remote Control and cloud sessions sync a transcript to a server, and
   `/feedback` uploads one; nothing else uploads a local session.
10. `ANTHROPIC_BASE_URL` sends Messages API requests to a gateway.
11. `--setting-sources project` leaves user and local settings out,
    including their allow rules, while `--settings` still loads its file.

An upgrade of Claude Code runs the fixture before it is used. A
behavior that changes is fixed in this binding, never in a Toolkit or
adapter-core file.

---

## Running the fixture on Claude Code

The [fixture session](adapter-contract.md#fixture-session) needs these
harness commands. For Claude Code:

| Fixture need | Claude Code |
| --- | --- |
| List discovered skills (step 1) | The `system` init message of a `stream-json` run; to confirm that it lists skills |
| Headless turn in the role (steps 2 to 14) | `claude -p --agent drafting-table --setting-sources project --settings .claude/settings.drafting-table.json --mcp-config .claude/mcp.drafting-table.json --strict-mcp-config --permission-prompts none --output-format stream-json --include-hook-events "<intent>"`; add `--continue` or `--resume <id>` to continue |
| Headless turn outside the role (steps 2, 7, 9) | The same without `--agent`, `--setting-sources`, `--settings`, and the MCP flags |
| Resolved native rules | No command prints them; the fixture keeps the three settings and MCP files next to the export |
| Session export (step 15) | The transcript JSONL and the `stream-json` output; no redacted form exists |

**Replayed model.** `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` with a
placeholder `ANTHROPIC_API_KEY` sends every request to a local endpoint
that speaks the Messages API and answers with the next recorded turn.
`--bare` is not used, because it skips hooks. The fixture environment
sets `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`.

**Binding checks** added to the harness-neutral ones:

- The `system` init message lists `wms` as the only MCP server.
- The `skills` of the `system` init message in the role are exactly the
  manifest's `toolkit_skills`. A name outside them means a new built-in
  or project skill that `skillOverrides` must turn off.
- No `stream-json` run contains a permission prompt or an automatic
  denial of a shell operation.
- The transcript of every run holds the adapter line as the first
  assistant text.

---

## Open points

The fixture must confirm these before any obligation is marked met:

1. Whether an agent with a `tools` list still receives the MCP tools of
   `--mcp-config`.
2. Whether `env` from a `--settings` file reaches a hook command, so
   `PROTOBOT_ROLE` is the role signal. If not, `agent_type` in the hook
   input is the fallback, and it is undocumented.
3. Whether agent-frontmatter `hooks` fire when the agent is the main
   session. The binding uses project-settings hooks and does not depend
   on it.
4. The exact path form of `Read` and `Edit` rules for `.protobot/**`
   and `.env`.
5. Whether project hooks run in `-p` mode without a trust step.
6. Whether a replay endpoint that speaks the Messages API satisfies
   `-p` end to end.
7. Whether the transcript names the agent, so step 15 can show the
   role.
8. Whether the set of built-in skills changes with the account or its
   enabled features, and not only with the version. The init-message
   check above catches both.

---

## Related Documents

- [Agent Harness Adapter Contract](adapter-contract.md) — The
  harness-neutral contract this binding implements.
- [OpenCode Harness Binding](opencode.md) — The sibling binding, the
  only one checked so far.
- [Codex Harness Binding](codex.md) — The sibling binding for Codex.
- [Git and Project-Repository Integration](../git-integration.md) —
  Permitted Git operations and ungoverned-edit detection.
- [Architecture](../../architecture.md) — The Drafting Table Boundary
  and the OpenCode-plus-skill strawman.
- [System Components](../components.md) — The Drafting Table and the
  Specification Toolkit.
- [Overview](../overview.md) — Single-player and multi-player modes, and
  platform.
