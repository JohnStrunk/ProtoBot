---
name: "pull-request"
description: >
  Provides a structured process for creating pull requests use when asked to
  "make a PR".
---

# Creating a Pull Request

When asked to "make a PR" or "create a pull request", follow this structured
process:

- Rebase your branch on the latest `upstream/main` to ensure it is up to date
- Ensure all tests pass and the code is ready for review
- Create the Pull Request on GitHub in a draft state
- Monitor CI checks and address any issues
- Await coderabbit's review and thoughtfully address any feedback. You can
  look at the CodeRabbit status check to see if the review is complete.
  - For each CodeRabbit comment, decide whether to address it or not based on
    the intended behavior of the change.
  - Whether or not you implement a change, respond to the comment with a clear
    explanation of your decision. Start your response with "(AI generated)".
- Also address feedback from `fullsend-ai-review[bot]` if applicable, and
  respond to their comments in the same manner.
- Once all checks and reviews are passed, perform a final rebase of the branch
  on the latest `upstream/main`
- Ensure CI checks pass after rebasing
- Mark the PR as ready for review: `gh pr ready <id>`
- Wait for the post-rebase CodeRabbit and `fullsend-ai-review[bot]` checks,
  addressing and responding to any feedback as described above.
- Enable auto-merge: `gh pr merge <id> --auto --merge`
- STOP. You have completed the pull request process.
