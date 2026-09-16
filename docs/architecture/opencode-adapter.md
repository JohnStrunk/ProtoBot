# ProtoBot: OpenCode Adapter

> Design document — September 2026
>
> The first harness binding of the
> [Specification Toolkit Harness Adapters](harness-adapters.md)
> contract: the OpenCode files, how they meet each obligation, and the
> OpenCode behaviors they rely on.

**Contents:**

- [Purpose and scope](#purpose-and-scope)
- [Binding files](#binding-files)
- [Skill discovery and invocation](#skill-discovery-and-invocation)
- [Obligation status](#obligation-status)
- [Observed OpenCode behaviors](#observed-opencode-behaviors)
- [Running the fixture on OpenCode](#running-the-fixture-on-opencode)
- [Related Documents](#related-documents)

---

## Purpose and scope

Issue #33 asks for the smallest useful Drafting Table adapter when the
user invokes ProtoBot inside an existing OpenCode session. The
harness-neutral half of the answer — the three layers, the manifest,
the Drafting Table role, the shell operations, the guard, session
behavior, traces, resumable state, exit conditions, and the fixture —
is in [Specification Toolkit Harness Adapters](harness-adapters.md).
This document is the OpenCode half.

It adds no rule of its own. Where OpenCode forces a choice, this
document states the choice and the obligation it serves. Every
OpenCode behavior named here was observed on OpenCode 1.18.30 with a
replayed model ([Observed OpenCode behaviors](#observed-opencode-behaviors)).

---

## Binding files

### Installed files

The binding is four files. At project scope they sit beside the shared
layer:

```text
<project root>/
├── opencode.json                        rules for every OpenCode agent
├── .opencode/
│   ├── agents/drafting-table.md         the Drafting Table role
│   ├── commands/drafting-table.md       the drafting-table entry point
│   └── plugins/protobot-guard.js        the shim that calls the guard
└── .agents/                             shared layer (harness-neutral)
    ├── drafting-table.yaml
    └── skills/
```

At user scope the same four files go under `~/.config/opencode/`.
Project config is merged after user config, so a project that carries
the binding overrides a user-scope install. A user-scope install also
needs the `external_directory` rule in
[How the rules combine](#how-the-rules-combine).

### `opencode.json`

```json
{
  "$schema": "https://opencode.ai/config.json",
  "share": "disabled",
  "mcp": {
    "ears-manager": {
      "type": "local",
      "command": ["<ears-manager MCP server, named by #30>"]
    },
    "wms": {
      "type": "local",
      "command": ["<WMS Adapter MCP server, named by #31>"]
    }
  },
  "permission": {
    "edit": {
      ".protobot/**": "deny"
    },
    "ears-manager_*": "deny",
    "wms_*": "deny"
  }
}
```

- **`share` is `disabled`**, because OpenCode's share feature uploads a
  session (H11).
- **The two MCP servers** are the manifest's `governed_mcp_servers`
  (H2). OpenCode names their tools `<server>_<tool>`, so the tools are
  `ears-manager_*` and `wms_*`. The command values are placeholders
  until #30 and #31 name the servers. Neither entry holds a credential.
- **`edit` is denied under `.protobot/` for every agent.** The `edit`
  rule covers every OpenCode file tool: `edit`, `write`, and the patch
  tool. It is a static copy of the guard's guarded-path rule that holds
  even when plugins do not load.
- **The governed tools are denied here and allowed only in the
  `drafting-table` agent**, a native copy of guard rule 4.

This file adds denies and nothing else;
[How the rules combine](#how-the-rules-combine) explains why.

### The `drafting-table` agent

`.opencode/agents/drafting-table.md` gives the Drafting Table role to
one primary agent (H3):

```markdown
---
description: ProtoBot Drafting Table — Sketching and Dimensioning through governed tools
mode: primary
permission:
  "*": deny
  read:
    "*": allow
    "*.env": deny
    "*.env.*": deny
    ".protobot/**": deny
  glob: allow
  grep: allow
  question: allow
  todowrite: allow
  skill:
    "*": deny
    drafting-specifications: allow
    eliciting-requirements: allow
  "ears-manager_*": allow
  "wms_*": allow
  bash:
    "*": deny
    # the native copy of the shell operations, below
---

You are the ProtoBot Drafting Table, running in OpenCode with the
OpenCode binding of adapter layout 1.

Load the drafting-specifications skill before anything else, and
follow it.

Toolkit skills name operations. In OpenCode:
- the operation `ears-manager <group> <verb>` is the tool
  `ears-manager_<group>_<verb>`;
- a WMS operation is the tool `wms_<operation>`; and
- a Git or Git host operation is one shell command.
```

- **`"*": deny` hides tools (H9).** OpenCode does not offer the model a
  built-in tool that has no allow rule, so the agent never sees `edit`,
  `write`, the patch tool, `task`, `webfetch`, or `websearch`. The same
  catch-all denies `doom_loop`, so an identical call repeated three
  times is refused rather than asked about.
- **The `skill` rule is the manifest's `toolkit_skills` (H10).** A new
  Toolkit skill adds one line here and one in the manifest; the fixture
  checks that the two lists match.
- **No rule is `ask`.** In `opencode run`, a rule that resolves to `ask`
  is rejected automatically and the run ends, so an agent with only
  allow and deny rules behaves the same headless and in the TUI (H7).
- **The prompt holds OpenCode facts only:** the binding and layout, the
  skill to load, and the tool-name mapping. The refusal rule, the
  resume triggers, and the recording notice are in the session skill.

#### Native copy of the shell operations

The `bash` block copies the harness-neutral
[shell operations](harness-adapters.md#shell-operations) into OpenCode
patterns, with the defaults `origin`, `cs/`, and `main`. The guard
enforces the same operations with the project's real values; this copy
refuses early and still holds when plugins do not load.

```yaml
bash:
  "*": deny
  # Read repository state
  "git status*": allow
  "git log*": allow
  "git diff*": allow
  "git show*": allow
  "git ls-files*": allow
  "git rev-parse*": allow
  "git merge-base*": allow
  "git remote -v": allow
  "git remote get-url origin": allow
  "git fetch origin*": allow
  # Work on a change-set branch
  "git switch -c cs/*": allow
  "git switch cs/*": allow
  "git add -- *": allow
  "git commit -F -*": allow
  "git merge --no-ff *": allow
  "git merge --abort": allow
  "git checkout -- *": allow
  "git push origin cs/*": allow
  "git push origin --delete cs/*": allow
  "git branch -d cs/*": allow
  # Pull requests and registration
  "gh pr create *": allow
  "gh pr edit *": allow
  "gh pr view*": allow
  "gh pr checks*": allow
  "gh pr merge * --merge*": allow
  "register-approved-change-set *": allow
  # Forbidden forms of the commands above
  "git * --output*": deny
  "git * --upload-pack*": deny
  "git * --receive-pack*": deny
  "git * --exec*": deny
  "git fetch *:*": deny
  "git add -- .": deny
  "git checkout -- .": deny
  "git commit *--amend*": deny
  "git commit *--all*": deny
  "git commit -F - -a*": deny
  "git merge *--squash*": deny
  "git merge *--ff-only*": deny
  "git push *:*": deny
  "git push *+*": deny
  "git push *--force*": deny
  "gh pr merge *--squash*": deny
  "gh pr merge *--rebase*": deny
  "gh pr merge *--admin*": deny
  "gh pr merge *--auto*": deny
```

OpenCode matches these patterns against the whole command text,
here-document bodies included, but does not look inside an output
redirection. `git log --oneline > docs/vision.md` matches `git log*`,
so the copy alone would let that command empty the file. The guard
refuses it.

### The `drafting-table` command

`.opencode/commands/drafting-table.md` is the entry point (H3, H4):

```markdown
---
description: Start or resume a ProtoBot Drafting Table session
agent: drafting-table
---

Start or resume a Drafting Table session with the
drafting-specifications skill. The user's intent, if given: $ARGUMENTS

Adapter: ProtoBot adapter layout 1, OpenCode binding.
```

The last line puts the adapter layout and the binding into the session
record ([Traces](harness-adapters.md#traces)).

### The guard shim

`.opencode/plugins/protobot-guard.js` connects OpenCode to the shared
[guard](harness-adapters.md#the-guard) (H8). OpenCode has no
`PreToolUse` command hook, so the shim does the translation:

1. **Tracks the role.** OpenCode's `tool.execute.before` hook does not
   name the agent, but `chat.params` does. The shim records the agent of
   each session: `drafting-table` is the Drafting Table role, and every
   other agent is `other`.
2. **Builds the guard input.** For each tool call it writes the
   `PreToolUse` JSON — `hook_event_name`, `session_id`, `cwd`,
   `tool_name`, and `tool_input` from the call's arguments — and runs
   `drafting-table-guard --harness opencode --role <role>`.
3. **Blocks on refusal.** On any non-zero status it throws an error
   whose message is the guard's line. OpenCode returns that message to
   the model as the tool result and keeps it in the session record.

The shim holds no rule, reads no project file, and does nothing when a
session is idle or ends (H5).

### How the rules combine

OpenCode resolves each agent's permissions into one ordered list, and
the last matching rule wins. The order is: OpenCode's defaults, the
built-in agent's own rules, the `permission` block of the config, then
the agent file's `permission` block. Three consequences shape the
binding:

| Trap | What happens | Rule in this binding |
| --- | --- | --- |
| A catch-all allow in `opencode.json` | It follows the built-in `plan` agent's `edit` deny, and gives `plan` write access | `opencode.json` adds denies only |
| A catch-all allow in an agent file | It follows OpenCode's default `ask` for `.env` files, and lets the agent read them | The agent denies `*.env` and `*.env.*` again |
| A catch-all deny in an agent file | It follows the allow that OpenCode adds for each discovered skill's directory, so a skill outside the project cannot read its own `references/` | A user-scope install adds `external_directory` allow for `~/.agents/skills/*` to the agent |

A user's own agent file can still override the project rules, because
an agent's rules come last. That agent is not the Drafting Table, and
the guard still applies to it.

---

## Skill discovery and invocation

### Discovery

OpenCode discovers skills without configuration (H1). It walks up from
the working directory to the root of the Git working tree and reads
`<name>/SKILL.md` under `.opencode/skills/`, `.claude/skills/`, and
`.agents/skills/`. It also reads `~/.config/opencode/skills/`,
`~/.claude/skills/`, and `~/.agents/skills/`. The Toolkit's
`.agents/skills/` is therefore read with no link. In the ProtoBot
repository, `.claude/skills` also links to `.agents/skills`; OpenCode
finds each skill twice and lists it once.

Discovery is not permission. The ProtoBot repository also holds
maintenance skills such as `pull-request` and `review-pr`, and OpenCode
ships a built-in skill. The agent's `skill` rule allows only the
manifest's Toolkit skills, and a denied skill cannot be loaded.

### Invocation

| Entry point | Effect |
| --- | --- |
| `/drafting-table [intent]` in a running session | The normal route. The command prompt runs on the `drafting-table` agent. |
| Selecting `drafting-table` with Tab | The same agent without the start prompt; its first turn loads the session skill |
| `opencode --agent drafting-table` | A new TUI session on the agent |
| `opencode run --agent drafting-table --command drafting-table` | A headless session; the fixture uses it |

Later turns must also run on `drafting-table`. A turn on another agent
has no governed tools, and the guard refuses them.

### The first consumer in OpenCode

`eliciting-requirements` loads through the `skill` tool and reads its
`references/` through `read`. Its evaluation wrapper in PR #64 runs
OpenCode with its own config and invokes it as
`/eliciting-requirements`, so the skill needs no binding file, and the
entry point's name must differ from every skill name.

---

## Obligation status

| # | Obligation | OpenCode binding | Status |
| --- | --- | --- | --- |
| H1 | Discover Toolkit skills from `.agents/skills/` | Native discovery | Met |
| H2 | Expose governed MCP tools to the role | `mcp` entries; `ears-manager_*` and `wms_*` rules | Met |
| H3 | `drafting-table` entry point | The command and the agent | Met |
| H4 | Resume on every entry, continued session, and compaction | Command prompt and session skill; `--continue` and `--session` continue a session | Met |
| H5 | Nothing on idle or exit | The shim registers no idle or exit hook | Met |
| H6 | Replayable session record | OpenCode's session record and `opencode export` | Met; the resolved rules come from `opencode debug agent` |
| H7 | Headless replay with no permission prompt | `opencode run --format json`, a replay provider, no `ask` rules | Met |
| H8 | Guard before every tool call | The shim | Met |
| H9 | Hide file-writing, subagent, and web tools | `"*": deny` | Met |
| H10 | Toolkit skills only | `skill` rule | Met |
| H11 | No credential in binding files; no session upload | Placeholders; `share: disabled` | Met |
| H12 | Publish this status | This table | Met |

What the OpenCode layer stops, by route:

| Write route to a guarded path | `drafting-table` agent | Every other agent |
| --- | --- | --- |
| File tool under `.protobot/` | Tool not offered | Refused by the project rule and the guard |
| File tool on a registered path elsewhere | Tool not offered | Refused by the guard |
| Shell writer, such as `sed -i` | Refused by the native copy and the guard | Not stopped |
| Output redirection in a shell command | Refused by the guard | Refused by the guard when the command contains the path as written from the project root |
| Tool of another MCP server | Not offered | The user's own configuration |
| Subagent | `task` not offered | Not applicable |

The routes that are not stopped are caught by the later layers, as
[What the harness layer stops](harness-adapters.md#what-the-harness-layer-stops)
describes.

---

## Observed OpenCode behaviors

The binding relies on these behaviors, each observed on OpenCode
1.18.30:

1. Skills are discovered from the locations in [Discovery](#discovery),
   and a name found twice is listed once.
2. A skill denied by the `skill` rule cannot be loaded.
3. A built-in tool with no allow rule for the agent is not offered to
   the model.
4. Rules resolve in the order and with the effects in
   [How the rules combine](#how-the-rules-combine).
5. Shell patterns match the whole command text, here-document bodies
   included. Commands joined with `;` are checked one by one. An output
   redirection inside an allowed command is not checked.
6. In `opencode run`, a rule that resolves to `ask` is rejected and the
   run ends.
7. An error thrown in the `tool.execute.before` plugin hook blocks the
   call, and its text reaches the model and the session record. The
   `chat.params` hook carries the agent name.
8. `opencode export` writes the session record as JSON with the agent,
   the model and provider, the OpenCode version, and every tool call.
   `opencode export --sanitize` redacts message text, tool arguments,
   and tool results, and keeps tool names, statuses, and error texts.
9. A custom OpenAI-compatible provider can serve recorded model turns
   to `opencode run`.

Issue #77 pins the OpenCode version it tests. An upgrade runs the
fixture before it is used. A behavior that changes is fixed in this
binding, never in a Toolkit or adapter-core file.

---

## Running the fixture on OpenCode

The [fixture session](harness-adapters.md#fixture-session) needs three
harness commands. For OpenCode:

| Fixture need | OpenCode |
| --- | --- |
| List discovered skills (step 1) | `opencode debug skill` |
| Headless turn in the role (steps 2 to 14) | `opencode run --agent drafting-table --command drafting-table --format json`; add `--continue` or `--session <id>` to continue |
| Headless turn outside the role (steps 2, 7, 9) | `opencode run --agent build --format json` |
| Resolved native rules | `opencode debug agent drafting-table` and `opencode debug config` |
| Session export (step 15) | `opencode export <session>`, with and without `--sanitize` |

**Replayed model.** A local OpenAI-compatible endpoint returns the
recorded model turns in order. It is registered as a custom provider:

```json
{
  "provider": {
    "replay": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Fixture replay",
      "options": { "baseURL": "http://127.0.0.1:<port>/v1" },
      "models": { "turns": { "name": "turns" } }
    }
  },
  "model": "replay/turns",
  "small_model": "replay/turns"
}
```

The endpoint answers a request that offers tools with the next recorded
turn, and a request without tools, such as title generation, with a
short text. OpenCode then runs its real skill tool, rules, plugin, MCP
clients, and shell.

**Binding checks** added to the harness-neutral ones:

- `opencode debug config` shows `share` as `disabled`.
- `opencode debug agent drafting-table` shows no `ask` rule from the
  agent file, and its `skill` patterns equal the manifest's
  `toolkit_skills`.
- No headless run prints an automatic permission rejection.

---

## Related Documents

- [Specification Toolkit Harness Adapters](harness-adapters.md) — The
  harness-neutral contract this binding implements.
- [Git and Project-Repository Integration](git-integration.md) —
  Permitted Git operations and ungoverned-edit detection.
- [Architecture](../architecture.md) — The Drafting Table Boundary and
  the OpenCode-plus-skill strawman.
- [System Components](components.md) — The Drafting Table and the
  Specification Toolkit.
- [Overview](overview.md) — Single-player and multi-player modes, and
  platform.
