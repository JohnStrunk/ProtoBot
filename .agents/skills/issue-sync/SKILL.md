---
name: "issue-sync"
description: >
  Synchronize a GitHub issue tracker with the implementation plan by proposing
  missing issues, blocking relationships, issue comments, and tracking-issue
  updates. Use when asked to create or refresh project issues. Always run
  issue-audit first.
---

# Synchronize the GitHub Issue Tracker

Create missing GitHub issues, set blocking relationships, update existing
issues with comments, and refresh the tracking issue. Run the `issue-audit`
skill first to identify what needs doing.

## Tool choice

Use the GitHub MCP server for issue reads and writes when it is available. The
standard tools map as follows:

- `github_issue_read` with `method: "get"` reads an issue.
- `github_issue_write` with `method: "create"` creates an issue.
- `github_issue_write` with `method: "update"` updates an issue body.
- `github_add_issue_comment` adds a comment.

The standard MCP surface does not expose GitHub Projects v2 item-add/status
operations or the `addBlockedBy` GraphQL mutation. Use the `gh` templates for
those operations. If MCP is unavailable, use `gh` for every step.

## Step 1: Gather repository and project metadata

Run metadata commands with `set -euo pipefail`. Keep repository and project
owners separate.

1. **Repository** - run
   `gh repo view --json owner,name --jq '{owner: .owner.login, name: .name}'`
   and record `REPO_OWNER` and `REPO_NAME`.

2. **Milestones** - fetch the complete milestone list:

   ```bash
   set -euo pipefail
   MILESTONES="$(gh api --method GET --paginate --slurp \
     "repos/<REPO_OWNER>/<REPO_NAME>/milestones?state=all&per_page=100")"
   jq -e 'if type != "array" or any(.[]; type != "array")
     then error("milestone response is not a paginated array")
     else add
     end' <<<"$MILESTONES"
   ```

3. **Labels** - fetch every existing label:

   ```bash
   set -euo pipefail
   LABELS="$(gh api --method GET --paginate --slurp \
     "repos/<REPO_OWNER>/<REPO_NAME>/labels?per_page=100")"
   jq -e 'if type != "array" or any(.[]; type != "array")
     then error("label response is not a paginated array")
     else add | map({name})
     end' <<<"$LABELS"
   ```

   Do not create labels. Use only existing labels.

4. **GitHub Project** - find the project number:

   ```bash
   gh project list --owner <PROJECT_OWNER> --format json --limit 1000
   ```

   Obtain `PROJECT_OWNER` from project context or ask the user; never substitute
   `REPO_OWNER` without confirmation.

   Select one project and record its owner, name, and number. If the intended
   project is not uniquely identified, stop until the project selection is
   unambiguous. Do not assume the first project in the response is the target.
   If the response
   reaches the `--limit` value, rerun with a higher limit or ask the user for
   the exact project number before treating the list as complete.

   Fetch the project node ID, Status field ID, and option IDs:

   ```bash
   set -euo pipefail
   PROJECT_RESPONSE="$(gh api graphql \
      -F projectOwner="<PROJECT_OWNER>" \
      -F number="<PROJECT_NUMBER>" \
     -f query='
   query($projectOwner: String!, $number: Int!) {
     organization(login: $projectOwner) {
       projectV2(number: $number) {
         id
         field(name: "Status") {
           ... on ProjectV2SingleSelectField {
             id
             options { id name }
           }
         }
       }
     }
     }')"
   jq -e '
     if ((.errors // []) | length) > 0
       or .data.organization.projectV2 == null
       or .data.organization.projectV2.id == null
       or .data.organization.projectV2.field.id == null
     then error("project metadata is incomplete or has GraphQL errors")
     else {
       project_id: .data.organization.projectV2.id,
       status_field_id: .data.organization.projectV2.field.id,
       statuses: (.data.organization.projectV2.field.options
         | map({(.name): .id}) | add)
     }
     end' <<<"$PROJECT_RESPONSE" | jq -e '
      if (.statuses.Backlog
        and .statuses.Ready
        and .statuses["In progress"]
        and .statuses["In review"]
        and .statuses.Done)
      then .
      else error("required project status option is missing")
      end'
   ```

   For a user-owned project, replace `organization(login: $projectOwner)` and
   `.data.organization` with `user(login: $projectOwner)` and `.data.user`.

   Validate that the required project status options exist before proposing
   changes. If names differ from the workflow's Backlog, Ready, In progress,
   In review, and Done roles, ask the user for an explicit role-to-option
   mapping instead of inferring by option order.

With MCP, use issue/label reads with explicit repository values. The standard
server cannot list milestones, all labels, or Project v2 fields, so retain `gh`.

## Step 2: Confirm scope with the user

Before creating or modifying anything, present the complete proposed change
set and get explicit approval. Include for every proposed issue:

- Title, labels, and milestone.
- Project owner, project name, and project number.
- Related issues and blocking relationships.
- The complete proposed body for every new issue.
- Use title or plan-ID references in new bodies until created issue numbers
  exist; do not approve unresolved `#<N>` placeholders.
- The exact comment for each ordinary existing-issue update; ordinary issue
  bodies are not edited.
- Use title or plan-ID references in comments until new issue numbers exist;
  present rendered number-based comments before posting them.
- The complete replacement body if the tracking issue will be rebuilt after
  created issue numbers are known.

Do not create issues, add dependencies, post comments, or edit the tracking
issue before approval.

## Step 3: Create approved issues

Use this command template for each approved issue. Populate these variables
from structured approved input or safe files; never paste approved text into
shell source. This keeps quotes, backticks, and `$()` literal:

```bash
set -euo pipefail

: "${TITLE:?set the approved title}"
: "${BODY:?set the approved body}"
ARGS=(--repo "$REPO_OWNER/$REPO_NAME" --title "$TITLE" --body "$BODY")
[ -n "${LABELS:-}" ] && ARGS+=(--label "$LABELS")
[ -n "${MILESTONE_NAME:-}" ] && ARGS+=(--milestone "$MILESTONE_NAME")
gh issue create "${ARGS[@]}"
```

With MCP, use `github_issue_write` like this instead:

```text
method: "create"
owner: "<REPO_OWNER>"
repo: "<REPO_NAME>"
title: "<TITLE>"
body: "<BODY>"
labels: ["<LABEL_1>", "<LABEL_2>"]
milestone: <MILESTONE_NUMBER>
```

Record every created issue number. Add each approved issue to the project:

```bash
gh project item-add <PROJECT_NUMBER> \
  --owner <PROJECT_OWNER> \
  --url "https://github.com/<REPO_OWNER>/<REPO_NAME>/issues/<ISSUE_NUMBER>"
```

The standard MCP tools do not expose this Project v2 item-add operation.

## Step 4: Set approved blocking relationships

Read [references/issue-sync-commands.md](references/issue-sync-commands.md) and
run its lookup and mutation blocks
in the same shell. It preserves the issue-number map, validates every lookup,
skips only duplicate/cycle errors, and aborts on all other failures. There is
no standard MCP equivalent for `addBlockedBy`.

## Step 5: Update approved existing issues

Add comments for updated dependencies, scope clarification, current state,
and implementation breakdowns:

Replace any title/plan-ID references with created issue numbers and present the
final comments before posting them.

```bash
gh issue comment <ISSUE_NUMBER> \
  --repo "$REPO_OWNER/$REPO_NAME" \
  --body "$(cat <<'EOF'
## <SECTION_TITLE>

<COMMENT_CONTENT>
EOF
)"
```

With MCP, call `github_add_issue_comment` with:

```text
owner: "<REPO_OWNER>"
repo: "<REPO_NAME>"
issue_number: <ISSUE_NUMBER>
body: "<COMMENT_CONTENT>"
```

Do not edit ordinary issue bodies; use comments for issue updates. Reserve
body replacement for the tracking issue in Step 6.

## Step 6: Update the tracking issue

If a pinned tracking issue exists, rebuild its body to include:

1. All new issues in the appropriate sections.
2. Checkboxes reflecting current completion state.
3. An open PRs section noting coverage.
4. An updated Mermaid dependency graph using `graph LR`.
5. The updated critical path and parallel work streams.

After creating issues, replace title/plan-ID references with the actual issue
numbers, present the complete rendered body to the user, and wait for the user's
decision on that final replacement. Then replace its body:

```bash
gh issue edit <TRACKING_ISSUE> \
  --repo "$REPO_OWNER/$REPO_NAME" \
  --body "$(cat <<'BODY'
<FULL_TRACKING_ISSUE_BODY>
BODY
)"
```

With MCP, use the complete call below. Do not send a partial body when the
intent is to rebuild the tracker:

```text
method: "update"
owner: "<REPO_OWNER>"
repo: "<REPO_NAME>"
issue_number: <TRACKING_ISSUE>
body: "<FULL_TRACKING_ISSUE_BODY>"
```

## Step 7: Report

Produce a summary:

1. **Created** - table of issue number, title, labels, and milestone.
2. **Dependencies set** - table of blocked issue, blocking issue, and result.
3. **Updated** - table of issue number and what changed.
4. **Tracking issue** - confirm it was updated or explain why it was not.
