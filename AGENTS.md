# AGENTS

- Create worktrees in `.worktrees/`
- All pre-commit tests must pass before committing changes.
- The upstream repository is `redhat-et/protobot`. Ensure that pull requests
  are made against this repository.
- Agent skills live in `.agents/skills/`. `.claude/skills` is a symlink to
  that directory so Claude Code finds the same skills; do not add skills
  under `.claude/` directly.
