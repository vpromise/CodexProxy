# Issue tracker: GitHub

Issues and specs live in [vpromise/CodexProxy](https://github.com/vpromise/CodexProxy/issues). Use the `gh` CLI and explicitly pass `--repo vpromise/CodexProxy` so operations target this fork.

## Conventions

- Create: `gh issue create --repo vpromise/CodexProxy --title "..." --body-file <body-file>`.
- Read: `gh issue view <number> --repo vpromise/CodexProxy --json number,title,body,labels,comments`.
- List: `gh issue list --repo vpromise/CodexProxy --state open --json number,title,body,labels,assignees`, with appropriate label filters and pagination when all results are needed.
- Update the body: `gh issue edit <number> --repo vpromise/CodexProxy --body-file <body-file>`.
- Comment: `gh issue comment <number> --repo vpromise/CodexProxy --body-file <body-file>`.
- Apply or remove labels: `gh issue edit <number> --repo vpromise/CodexProxy --add-label "..."` or `--remove-label "..."`. Use the role mapping in `triage-labels.md`.
- Close: `gh issue close <number> --repo vpromise/CodexProxy`.
- Link a child: create with `--parent <parent-number>`, or run `gh issue edit <parent-number> --repo vpromise/CodexProxy --add-sub-issue <child-number>`.

Write multiline bodies to a temporary UTF-8 file and pass it with `--body-file`, preserving actual newlines. If sub-issues are unavailable, put `Part of #<parent>` at the top of the child body and add the child to a task list in the parent body.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## Skill operations

- **Publish to the issue tracker**: create a GitHub issue in `vpromise/CodexProxy`.
- **Fetch the relevant ticket**: read the referenced GitHub issue with its labels and comments.

## Wayfinding operations

- **Map**: one issue labelled `wayfinder:map`, with Notes, Decisions-so-far, and Fog sections.
- **Child ticket**: a sub-issue of the map, labelled `wayfinder:<type>` where type is `research`, `prototype`, `grilling`, or `task`. Create missing wayfinder labels when this workflow is first used.
- **Blocking**: use native issue dependencies: `gh issue edit <child-number> --repo vpromise/CodexProxy --add-blocked-by <blocker-number>`. If unavailable, record `Blocked by: #<number>, #<number>` near the top of the child body.
- **Frontier**: list the map's open children from its sub-issues or task list. A ticket is eligible when it has no assignee and every blocker is closed; choose the first eligible ticket in map order.
- **Claim**: `gh issue edit <number> --repo vpromise/CodexProxy --add-assignee @me` before starting work on the ticket.
- **Resolve**: comment with the answer, close the child issue, then append a short summary and link to the map's Decisions-so-far section.
