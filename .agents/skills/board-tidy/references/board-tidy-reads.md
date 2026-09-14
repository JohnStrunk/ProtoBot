# Board Read Templates

Use this template in Step 2 after the project metadata and milestone mode are
known. Fetch every project page and validate the complete response before
filtering. An empty milestone array is valid; the item query still requires a
complete project response.

## Project Items Query

```bash
set -euo pipefail

gh api graphql --paginate --slurp \
  -F projectOwner="<PROJECT_OWNER>" \
  -F number="<PROJECT_NUMBER>" \
  -f query='
query($projectOwner: String!, $number: Int!, $endCursor: String) {
  organization(login: $projectOwner) {
    projectV2(number: $number) {
      items(first: 100, after: $endCursor) {
        nodes {
          id
          fieldValueByName(name: "Status") {
            ... on ProjectV2ItemFieldSingleSelectValue {
              name
              optionId
            }
           }
           content {
             __typename
             ... on Issue {
              number
              title
              state
              author { login }
              repository {
                name
                owner { login }
              }
              milestone { number title state }
              parent { number title }
              blockedBy(first: 100) {
                nodes { number state }
                totalCount
              }
            }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}' | jq -e --arg repoOwner "<REPO_OWNER>" --arg repoName "<REPO_NAME>" '
  if type != "array"
    or any(.[]; ((.errors // []) | length) > 0)
    or any(.[]; .data.organization.projectV2 == null)
    or any(.[]; (.data.organization.projectV2.items.nodes | type) != "array"
      or (.data.organization.projectV2.items.pageInfo.hasNextPage | type)
        != "boolean"
      or ((.data.organization.projectV2.items.pageInfo.hasNextPage
        and (.data.organization.projectV2.items.pageInfo.endCursor | type)
          != "string")))
    or length == 0
    or .[-1].data.organization.projectV2.items.pageInfo.hasNextPage != false
    or any(.[]; any(.data.organization.projectV2.items.nodes[];
      .content.__typename == "Issue"
      and ((.id | type) != "string"
        or (.content.number | type) != "number"
        or (.content.title | type) != "string"
        or (.content.state as $state
          | ($state != "OPEN" and $state != "CLOSED"))
        or (.content.author != null
          and (.content.author.login | type) != "string")
        or (.content.repository.name | type) != "string"
        or (.content.repository.owner.login | type) != "string"
        or (.content.milestone != null
          and ((.content.milestone.number | type) != "number"
            or (.content.milestone.title | type) != "string"
            or (.content.milestone.state != "OPEN" and
              .content.milestone.state != "CLOSED")))
        or (.content.parent != null
          and ((.content.parent.number | type) != "number"
            or (.content.parent.title | type) != "string"))
        or (.content.blockedBy | type) != "object"
        or (.content.blockedBy.nodes | type) != "array"
        or (.content.blockedBy.totalCount | type) != "number"
        or any(.content.blockedBy.nodes[];
          (.number | type) != "number"
          or (.state != "OPEN" and .state != "CLOSED")))))
  then error("project response is incomplete or contains GraphQL errors")
  else
  [.[].data.organization.projectV2.items.nodes[]
    | select(.content.__typename != "Issue")
    | {item_id: .id, content_type: (.content.__typename // "unknown")}
  ] as $nonIssues
  | [.[].data.organization.projectV2.items.nodes[]
    | select(.content.number != null)
    | {
        item_id: .id,
        repository_owner: (.content.repository.owner.login // ""),
        repository_name: (.content.repository.name // ""),
        status: (.fieldValueByName // {}).name,
        status_option_id: (.fieldValueByName // {}).optionId,
        number: .content.number,
        title: .content.title,
        state: .content.state,
        author_login: (.content.author.login // null),
        milestone: (.content.milestone // {}).title,
        milestone_number: (.content.milestone // {}).number,
        milestone_state: (.content.milestone // {}).state,
        parent: (.content.parent // null),
        blocked_by: [(.content.blockedBy.nodes // [])[]
          | {number, state}],
        open_blocker_count: ([.content.blockedBy.nodes[]
          | select(.state == "OPEN")] | length),
        blockers_complete: (.content.blockedBy.totalCount
          == (.content.blockedBy.nodes | length)
          and all(.content.blockedBy.nodes[];
            .number != null
            and (.state == "OPEN" or .state == "CLOSED")))
      }
  ] as $items
  | {
      items: [$items[]
        | select(.repository_owner == $repoOwner
          and .repository_name == $repoName)],
      excluded_cross_repository: [$items[]
        | select(.repository_owner != $repoOwner
          or .repository_name != $repoName)],
      excluded_non_issue: $nonIssues
     }
  end'
```

## Project Variants

For a user-owned project, replace the GraphQL root with
`user(login: $projectOwner)` and every `.data.organization` path with
`.data.user`.

## Follow-Up Details

Use `github_issue_read(owner: "<REPO_OWNER>", repo: "<REPO_NAME>",
issue_number: <ISSUE_NUMBER>, method: "get")` only for follow-up details.
Report `excluded_cross_repository`; do not inspect or mutate those items.
