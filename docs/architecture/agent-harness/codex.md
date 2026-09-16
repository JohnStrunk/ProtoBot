# ProtoBot: Codex Harness Binding

> Design document — September 2026
>
> The third harness binding of the
> [Agent Harness Adapter Contract](adapter-contract.md): the Codex files,
> how they are meant to meet each obligation, and the Codex behaviors
> they rely on. Designed against Codex CLI 0.154.0; the fixture has not
> run on it.

**Contents:**

- [Purpose and scope](#purpose-and-scope)
- [Binding files](#binding-files)
- [Skill discovery and invocation](#skill-discovery-and-invocation)
- [Obligation status](#obligation-status)
- [Codex behaviors](#codex-behaviors)
- [Running the fixture on Codex](#running-the-fixture-on-codex)
- [Open points](#open-points)
- [Related Documents](#related-documents)

---

## Purpose and scope

This document binds the
[Agent Harness Adapter Contract](adapter-contract.md) to the Codex CLI,
so the Drafting Table runs there with the same Toolkit, the same guard,
and the same fixture as in OpenCode and Claude Code. It adds no rule of
its own. Where Codex forces a choice, this document states the choice
and the obligation it serves.

The fixture has not run here yet. The behaviors marked "observed" in
[Codex behaviors](#codex-behaviors) were checked on 2026-09-16 with
`codex debug prompt-input`, which renders the model's prompt without a
model, and with headless runs against a local stub model that recorded
every request. The behaviors marked "documented" come from the Codex
documentation, the CLI help, and the source of version 0.154.0. The
[fixture](#running-the-fixture-on-codex) turns each "designed" status
into "met" or into a recorded gap, and [Open points](#open-points) lists
what it must confirm first.

Codex differs from the other two harnesses in three ways that shape
this binding:

- **It has no agent file for the main session.** Codex agent roles
  apply to spawned subagents only. The role is a configuration profile,
  and Codex reads a profile only from `$CODEX_HOME`.
- **It has no file-reading tool and no skill tool.** The model reads a
  file, a `SKILL.md` included, through its shell tool. The guard
  therefore counts a small set of read-only command forms as reads
  ([Read forms](#read-forms)).
- **Its hooks fail open.** A hook that is not trusted does not run, and
  a hook that exits with any status other than 2, prints nothing, or
  times out lets the call through. The binding closes what it can and
  records the rest.

---

## Binding files

### Installed files

The binding is two files. At project scope they sit beside the shared
layer:

```text
<project root>/
├── .codex/
│   ├── hooks.json                       the hook that calls the guard, every session
│   └── drafting-table.config.toml       the role profile, linked into $CODEX_HOME
└── .agents/                             shared layer (harness-neutral)
    ├── drafting-table.yaml
    └── skills/
```

Codex reads `.agents/skills/` natively, so no skill link exists.

Codex loads a project's `.codex/` files only when the user trusts the
project. `--profile <name>` reads `$CODEX_HOME/<name>.config.toml` and
nothing else: a profile file inside the project is ignored, and a
missing profile raises no error (observed). The install step therefore
links `$CODEX_HOME/drafting-table.config.toml`, by default
`~/.codex/drafting-table.config.toml`, to the project's file. The
profile names no project path, so one link serves every project that
uses adapter layout 1, and the fixture checks that the linked file
equals the project's.

At user scope, `hooks.json` goes to `$CODEX_HOME/hooks.json` and the
profile to `$CODEX_HOME/drafting-table.config.toml`.

The role is launched, not selected inside a session:

```text
PROTOBOT_ROLE=drafting-table codex --profile drafting-table "<intent>"
```

Codex has no documented way to switch a running session to another
profile. A user who is already in a session exits and resumes it with
the profile, as [Invocation](#invocation) shows.

### `.codex/hooks.json`

Project hooks apply to every Codex session in the project, whatever its
profile:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "sh -c 'command -v drafting-table-guard >/dev/null 2>&1 || { echo \"drafting-table-guard: not on PATH\" >&2; exit 2; }; exec drafting-table-guard --harness codex --role \"${PROTOBOT_ROLE:-other}\"'"
          }
        ]
      }
    ]
  }
}
```

- **The hook calls the guard before every tool call (H8).** Codex sends
  the `PreToolUse` input on standard input, in the shape the guard
  expects: `hook_event_name`, `session_id`, `cwd`, `tool_name`, and
  `tool_input`, plus `model`, `permission_mode`, `tool_use_id`,
  `transcript_path`, and `turn_id`. For a shell command, `tool_name` is
  `Bash` and the command is `tool_input.command` (observed). A file
  patch arrives as `apply_patch` and an MCP call as
  `mcp__<server>__<tool>` (documented).
- **Exit status 2 with a line on standard error blocks the call**, and
  the model gets "Command blocked by PreToolUse hook:" followed by that
  line (observed). The guard always writes the line, and the command
  exits 2 itself when the guard is missing, so a missing executable does
  not let calls through.
- **`PROTOBOT_ROLE` is the role signal.** The hook input names no
  profile. A hook command inherits the environment Codex was started
  with (observed), so the launch sets the variable and the command
  passes it to the guard. A launch without it runs as `other`, where
  the guard refuses every governed operation.
- **An untrusted hook does not run.** Project trust is not enough:
  Codex keeps a trust hash for each hook in the user configuration,
  under `hooks.state`, and a changed hook needs trust again. The user
  reviews and trusts the hook once with `/hooks`. In a trusted project
  with an untrusted `hooks.json`, a command that the hook refuses ran
  without any diagnostic (observed). The
  [fixture](#running-the-fixture-on-codex) checks that the hook fires
  before any other step.
- **A crash, an exit other than 2, or a timeout lets the call through**
  (documented). The guard exits 2 on any internal error, as the
  contract requires. A guard that hangs past the hook timeout is a gap
  the native layer cannot close.
- Every matching hook of every configuration layer runs, so a user's
  own hooks run beside this one and cannot replace it.

### The role profile

`.codex/drafting-table.config.toml` holds the role's native
configuration:

```toml
developer_instructions = """
You are the ProtoBot Drafting Table, running in Codex with the Codex
binding of adapter layout 1. Begin every start summary with the line
"Adapter: ProtoBot adapter layout 1, Codex binding."

Your skills are these Toolkit skills and no others. Open a skill by
reading its SKILL.md under .agents/skills/, and read its references the
same way:
- drafting-specifications: the session protocol. Open it first and
  follow it.
- eliciting-requirements: EARS elicitation, when the session protocol
  asks for it.

Toolkit skills name operations. In Codex:
- an `ears-manager` operation is one shell command with
  `--output json`, and its long text goes on standard input;
- a WMS operation is the tool `mcp__wms__<operation>`;
- a Git or Git host operation is one shell command; and
- a file is read with one of the read forms of the Codex binding.
"""
approval_policy = "never"
sandbox_mode = "danger-full-access"
web_search = "disabled"

[features]
multi_agent = false

[skills]
include_instructions = false

[mcp_servers.wms]
command = "<WMS Adapter MCP server, named by #31>"
default_tools_approval_mode = "approve"

[analytics]
enabled = false

[feedback]
enabled = false
```

`--strict-config` accepts every key of this profile (observed).

- **`developer_instructions` is the entry point's prompt (H3).** It
  holds Codex facts only: the binding and layout line, the Toolkit
  skills and where to open them, and the tool-name mapping. The adapter
  line is spoken in the first turn, so the session record holds it
  ([Traces](adapter-contract.md#traces)). The two skill names are the
  manifest's `toolkit_skills`, and the fixture checks that they match.
- **`[skills] include_instructions = false` hides every skill (H10).**
  See [Skill visibility](#skill-visibility).
- **`approval_policy = "never"` (H7).** A command that needs approval is
  rejected and the failure returns to the model, so the role behaves the
  same headless and interactive.
- **`sandbox_mode = "danger-full-access"`.** Codex's `workspace-write`
  sandbox keeps `.git`, `.agents`, and `.codex` read-only (documented),
  so `git add`, `git commit`, and `git merge` would fail in it. The role
  therefore runs commands without the Codex sandbox, as OpenCode and
  Claude Code do, and the guard bounds the shell.
- **`web_search = "disabled"` and `multi_agent = false` hide the web and
  subagent tools (H9).** With them, a model without an `apply_patch`
  tool type gets `exec_command`, `write_stdin`, `request_user_input`,
  and `view_image` (observed), plus the `wms` tools. Codex's hosted web
  search is not seen by hooks, so hiding it is the only control.
- **`apply_patch` cannot be hidden.** Codex offers it when the model's
  metadata names an `apply_patch` tool type, which OpenAI models do, and
  no setting removes it (documented). The guard refuses it in the role
  (guard rule 5), and H9 records the gap.
- **`[mcp_servers.wms]` exists only in the profile (H2).** Other
  sessions never load the server. `default_tools_approval_mode` lets the
  `wms` tools run under `approval_policy = "never"`. An MCP server from
  the user's base configuration still loads in the role, and guard
  rule 4 refuses its tools. In multi-player mode the entry becomes a
  streamable HTTP server with `url`, and the user authenticates once
  with `codex mcp login wms`, which keeps the token in Codex's own store
  outside the project (H13, unverified). The Web Drafting Table uses no
  harness binding
  ([Deployment modes](adapter-contract.md#deployment-modes)).
- **`[analytics]` and `[feedback]` are off (H11).** Analytics is on by
  default in `codex exec`, and feedback can upload a session. Codex
  Cloud and remote control run only when the user starts them.
- The profile holds no credential and no project path.

### Read forms

The contract's role may read project files. Codex offers no read tool
besides `view_image`, so the guard's Codex vocabulary row lists the
read-only shell forms that count as reads. Each is one simple command:

| Read | Command forms |
| --- | --- |
| Show a file or a part of it | `cat <path> ...`, `sed -n '<a>,<b>p' <path>`, `head -n <n> <path>`, `tail -n <n> <path>`, `nl -ba <path>` |
| Count lines | `wc -l <path> ...` |
| List a directory | `ls <path> ...`, `ls -la <path> ...`, `rg --files <path> ...` |
| Search | `rg -n <pattern> <path> ...` |

Every `<path>` is named explicitly and checked after symlink
resolution. The guard refuses a read form under `.protobot/`, of an
`.env` file, or outside the project except a user-scope Toolkit skill
root, and refuses a read of `<skill root>/<name>/SKILL.md` or of a file
below it when `<name>` is not in `toolkit_skills` (guard rule 5). A
search or listing must name a path, so a search of the whole project
root is refused.

### No execpolicy rules

Codex prefix rules in `.codex/rules/` load for every session in a
trusted project (observed), and the strictest matching decision wins.
A rule matches a command from its first word only, a command that no
rule matches is allowed, and an `allow` rule runs its command outside
the sandbox (documented). A native copy of the shell operations in
those rules would either be too weak to refuse an option after the
prefix, or would widen every other session. The binding adds no rules
file. The guard carries the shell operations (H8), as it does for
options inside a command in the Claude Code binding.

---

## Skill discovery and invocation

### Discovery

Codex discovers skills from `.agents/skills/` in the working directory
and every parent up to the project root, the first directory with
`.git`; from `.codex/skills/` in a trusted project; from
`~/.agents/skills/` and `$CODEX_HOME/skills/` for the user; from its
bundled system skills in `$CODEX_HOME/skills/.system`; from
`/etc/codex/skills/`; and from enabled plugins (H1, documented). A run
from the ProtoBot worktree listed the five bundled skills and every
skill in `.agents/skills/` (observed). Codex lists both entries when
one name exists twice, which is one reason skill rule 3 of the contract
demands unique names.

### Skill visibility

Discovery is not permission. Codex lists skills for the model in a
developer message, `<skills_instructions>`, with a name, a description,
and a path for each, and the model opens the `SKILL.md` itself.
`codex debug prompt-input` on 0.154.0, on 2026-09-16, showed:

| Setting | Skills in the model's prompt |
| --- | --- |
| None | Every discovered skill, bundled ones included |
| `[[skills.config]]` with `name` and `enabled = false` | That skill left out |
| `[[skills.config]]` with the path of its `SKILL.md` and `enabled = false` | That skill left out |
| `[[skills.config]]` with the path of the skill directory, or `name = "*"` | No effect |
| `[skills.bundled]` with `enabled = false` | Every bundled skill left out |
| `[skills] include_instructions = false` | No `<skills_instructions>` message at all |

`[[skills.config]]` works only by exact name or file path, like
`skillOverrides` in Claude Code, and Codex reads it only from user
layers: the user configuration, a profile, and `-c`, never a project's
`.codex/config.toml` (documented). A skill that the binding cannot name
in advance, in the user's own skill directories or in a plugin, would
stay listed.

The profile therefore turns the catalog off with
`include_instructions = false`, and its `developer_instructions` name
the two Toolkit skills and where to open them. The model's prompt then
names exactly the manifest's `toolkit_skills`, whatever else Codex
discovers. Unlike the Claude Code binding, no skill stays visible.

A hidden skill's files are still on disk, and Codex has no skill tool
to refuse a load. The guard refuses a read form on a `SKILL.md` outside
`toolkit_skills` ([Read forms](#read-forms)), so the model cannot load
another skill in the role. A user can still insert a skill by typing
`$<name>` in a prompt (documented); that is the user's own action, and
the guard does not see it.

### Invocation

| Entry point | Effect |
| --- | --- |
| `PROTOBOT_ROLE=drafting-table codex --profile drafting-table "<intent>"` | A new session in the role |
| `PROTOBOT_ROLE=drafting-table codex --profile drafting-table resume --last` | The most recent session continues in the role; the resume steps run again |
| `PROTOBOT_ROLE=drafting-table codex --profile drafting-table resume <id>` | The named session continues in the role |
| `PROTOBOT_ROLE=drafting-table codex exec --profile drafting-table --json "<intent>"` | A headless session; the fixture uses it |

A session without the profile has no `wms` tools, and one without
`PROTOBOT_ROLE` is `other` to the guard, which refuses every governed
operation.

### The first consumer in Codex

The profile names `eliciting-requirements`, and the model opens its
`SKILL.md` and `references/` with read forms. The skill needs no binding
file.

---

## Obligation status

| # | Obligation | Codex binding | Status |
| --- | --- | --- | --- |
| H1 | Discover Toolkit skills from `.agents/skills/` | Native discovery | Observed from the project root; the fixture has not run |
| H2 | The `ears-manager` CLI and the `wms` tools for the role | `exec_command`; `[mcp_servers.wms]` in the profile | Designed |
| H3 | `drafting-table` entry point | The profile, launched with `--profile` and `PROTOBOT_ROLE` | Observed that the profile loads and its instructions reach the model; a missing profile raises no error |
| H4 | Resume on every entry, continued session, and compaction | `codex resume` and `codex exec resume` with the profile; the session skill runs the resume steps | Designed |
| H5 | Nothing on idle or exit | No `Stop` or `SessionEnd` hook | Designed |
| H6 | Replayable session record | The session file under `$CODEX_HOME/sessions/` and the `codex exec --json` event stream | Designed; hook events are not recorded, and a refusal is recorded as the tool output |
| H7 | Headless replay with no permission prompt | `codex exec --json`, `approval_policy = "never"`, and a custom model provider pointed at a replay endpoint | Observed with a stub endpoint; the fixture has not run |
| H8 | Guard before every tool call | The project hook, trusted once with `/hooks` | Observed for shell commands; an untrusted hook does not run, and a hanging guard fails open |
| H9 | Hide file-writing, subagent, and web tools | `web_search = "disabled"`, `multi_agent = false` | Observed for web and subagent tools; `apply_patch` cannot be hidden and is refused by the guard (gap) |
| H10 | Toolkit skills only | `include_instructions = false`; the profile names the Toolkit skills; the guard refuses any other `SKILL.md` read | Observed that the catalog is gone; the fixture has not run |
| H11 | No credential in binding files; no session upload | Placeholders; `[analytics]` and `[feedback]` off; no `codex cloud` or `remote-control` | Designed |
| H12 | Publish this status | This table | Met |
| H13 | Remote `wms` server with OAuth 2.1 | `[mcp_servers.wms]` with `url` and `oauth`, and `codex mcp login wms` | Candidate; not checked |

"Designed" means the mechanism is documented and the fixture has not
run. "Observed" means a stub run or `codex debug prompt-input` showed
it. Nothing else is marked met.

What the Codex layer stops, by route:

| Write route to a guarded path | Role profile | Every other session |
| --- | --- | --- |
| `apply_patch` under `.protobot/` or on a registered path | Offered to OpenAI models; refused by the guard | Refused by the guard |
| Shell writer, such as `sed -i` | Refused by the guard | Not stopped |
| Output redirection in a shell command | Refused by the guard | Refused by the guard when the redirection target is written from the project root |
| Tool of another MCP server | Refused by the guard | The user's own configuration |
| Subagent | Not offered | Not applicable |

The native layer stops less than in the other two bindings: no Codex
rule refuses a command for the role alone, and no setting hides
`apply_patch`. The guard carries the difference, and the later layers
still hold ([What the harness layer stops][layer-stops]).

[layer-stops]: adapter-contract.md#what-the-harness-layer-stops

---

## Codex behaviors

The binding relies on these behaviors of Codex CLI 0.154.0.

**Observed on 2026-09-16:**

1. `codex debug prompt-input` renders the developer message
   `<skills_instructions>` without a model, with the bundled skills
   from `$CODEX_HOME/skills/.system` and the skills in the project's
   `.agents/skills/`.
2. `[[skills.config]]` with `enabled = false` hides a skill named by
   `name` or by the path of its `SKILL.md`; a directory path and
   `name = "*"` have no effect; `[skills.bundled]` with
   `enabled = false` hides every bundled skill; `[skills]` with
   `include_instructions = false` removes the whole message.
3. A request to a custom model provider with `wire_api = "responses"`
   carries the tools `exec_command`, `write_stdin`,
   `request_user_input`, `view_image`, `multi_agent_v1`, and
   `web_search`, and no `apply_patch` for a model slug Codex does not
   know. `web_search = "disabled"` removes `web_search`, and
   `features.multi_agent = false` removes `multi_agent_v1`.
4. `--profile <name>` layers `$CODEX_HOME/<name>.config.toml`, and its
   `developer_instructions` reach the model. A profile file in the
   project's `.codex/` is ignored, and a missing profile raises no
   error. `--strict-config` rejects an unknown key, such as
   `tools.view_image`, and accepts the role profile above.
5. A trusted project's `.codex/config.toml` and `.codex/rules/*.rules`
   load, and a `forbidden` prefix rule rejects its command.
6. A `PreToolUse` command hook receives a shell call as `tool_name`
   `Bash` with `tool_input.command`. Exit status 2 blocks the call and
   returns the hook's standard error to the model. The hook runs before
   the prefix rules. A variable set at launch reaches the hook command.
7. A project `.codex/hooks.json` that is not trusted does not run, even
   in a trusted project; `--dangerously-bypass-hook-trust` runs it.

**Documented:**

1. Skill discovery locations are the ones in [Discovery](#discovery);
   `[[skills.config]]` is read from user layers only; a `$<name>`
   mention inserts a skill into the prompt.
2. Hook trust is a hash per hook under `hooks.state` in the user
   configuration, reviewed with `/hooks`; a changed hook needs trust
   again. A hook that crashes, exits with a status other than 2, writes
   nothing to standard error, or times out lets the call through. Hook
   events are not written to the `--json` stream or the session file.
3. `apply_patch` reaches hooks as `apply_patch`, and an MCP tool as
   `mcp__<server>__<tool>`; hosted web search does not reach hooks.
4. `workspace-write` keeps `.git`, `.agents`, and `.codex` read-only;
   `approval_policy = "never"` rejects a command that needs approval.
5. A prefix rule matches from the first word; the strictest decision
   wins; an unmatched command is allowed; an `allow` rule runs the
   command outside the sandbox.
6. `codex resume` and `codex exec resume` continue a session. The
   session file is `$CODEX_HOME/sessions/<year>/<month>/<day>/rollout-*.jsonl`
   and records the developer instructions, every tool call, and every
   tool output.
7. `[mcp_servers.<name>]` registers a stdio server with `command` or a
   streamable HTTP server with `url`, and `codex mcp login <name>` runs
   its OAuth flow. `wire_api = "responses"` is the only model provider
   wire protocol.
8. Analytics is on by default in `codex exec`, and `/feedback` can
   upload a session; `[analytics]` and `[feedback]` with
   `enabled = false` turn them off.

An upgrade of Codex runs the fixture before it is used. A behavior
that changes is fixed in this binding, never in a Toolkit or
adapter-core file.

---

## Running the fixture on Codex

The [fixture session](adapter-contract.md#fixture-session) needs these
harness commands. For Codex:

| Fixture need | Codex |
| --- | --- |
| List discovered skills (step 1) | `codex debug prompt-input` from a subdirectory of the clone, without the profile; the names in `<skills_instructions>` |
| Headless turn in the role (steps 2 to 14) | `PROTOBOT_ROLE=drafting-table codex exec --profile drafting-table --strict-config --json "<intent>"`; `codex exec --profile drafting-table resume --last` or `resume <id>`, with the same variable, to continue |
| Headless turn outside the role (steps 2, 7, 9) | `codex exec --json "<prompt>"` |
| Resolved native rules | No command prints them; the fixture keeps the profile and `hooks.json` next to the export |
| Session export (step 15) | The session file under the fixture's `$CODEX_HOME/sessions/` and the `--json` event stream |

**Replayed model.** The fixture's own `$CODEX_HOME/config.toml` selects
a custom model provider with `model_provider`, defined as
`[model_providers.replay]` with `base_url` at a local endpoint and
`wire_api = "responses"`. Provider settings are not read from a
project's configuration. The endpoint speaks the Responses API and
answers with the next recorded turn. For a model slug Codex does not
know, Codex uses fallback metadata without `apply_patch`, so the
fixture also supplies model metadata that offers `apply_patch`, and
step 6 includes an `apply_patch` call. The same `config.toml` marks the
clone as trusted and holds the hook's trust entry.

**Binding checks** added to the harness-neutral ones:

- `--strict-config` accepts the profile, and the linked profile equals
  the project's file.
- The first recorded request in the role carries the adapter line in
  its developer instructions, which shows that the profile loaded.
- That request has no `<skills_instructions>` message, and its
  developer instructions name exactly the manifest's `toolkit_skills`.
- Its tools are `exec_command`, `write_stdin`, `request_user_input`,
  `view_image`, `apply_patch`, and the `wms` tools, and no other.
- A replayed command that the guard refuses, in the first turn of
  step 2, is refused, which shows that the hook is trusted and running.

---

## Open points

The fixture must confirm these before any obligation is marked met:

1. Whether the hook's trust entry can be written for the fixture
   without the interactive `/hooks` review. If not, the fixture runs
   with `--dangerously-bypass-hook-trust`, and a separate interactive
   check covers the trust step.
2. Whether `PreToolUse` fires for `write_stdin`, `view_image`, and
   `request_user_input`, and which `tool_name` each carries.
3. Whether the hook command runs through a shell, so that the command
   in `hooks.json` works as written.
4. Whether `default_tools_approval_mode = "approve"` lets the `wms`
   tools run headless under `approval_policy = "never"`.
5. Whether a resumed session keeps the profile's tools, instructions,
   and hidden skill catalog.
6. Whether the model, without a skill catalog, opens the Toolkit skills
   reliably from the profile's instructions. The fixture's replayed
   turns cannot show this; the skill evaluation (#63) can.

---

## Related Documents

- [Agent Harness Adapter Contract](adapter-contract.md) — The
  harness-neutral contract this binding implements.
- [OpenCode Harness Binding](opencode.md) — The first binding, the
  only one checked by the fixture so far.
- [Claude Code Harness Binding](claude-code.md) — The second binding.
- [Git and Project-Repository Integration](../git-integration.md) —
  Permitted Git operations and ungoverned-edit detection.
- [Architecture](../../architecture.md) — The Drafting Table Boundary
  and the OpenCode-plus-skill strawman.
- [System Components](../components.md) — The Drafting Table and the
  Specification Toolkit.
- [Overview](../overview.md) — Single-player and multi-player modes, and
  platform.
