---
name: "rebase-pr"
description: >
  Provides a structured process for rebasing pull requests use when asked to
  "rebase a PR".
---

# Rebasing a Pull Request

When asked to "rebase a PR", follow this structured process:

- Check out the branch associated with the pull request into a local worktree
- Rebase the branch on the latest `upstream/main`
- Resolve any merge conflicts that arise during the rebase process
- Ensure all tests pass locally after the rebase
- Push the rebased branch back to the remote repository, which will update the
  pull request
- Monitor CI checks and address any issues that arise after pushing the
  rebased branch
- Clean up the local worktree after the rebase process is complete
