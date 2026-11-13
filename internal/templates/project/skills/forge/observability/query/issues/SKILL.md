---
name: observability/query/issues
description: Filtering and assigning issues by person with `forge obs issues --assignee` and `forge obs issue assign` — how `me` is resolved from the verified credential (never a stored login), why an organization automation token is refused, how `none` maps to the API's unassigned flag, and what a local env answers.
---

# Issue assignment

```bash
forge obs issues prod --assignee me       # the credential's acting person
forge obs issues prod --assignee none     # only unassigned issues
forge obs issue assign ISS-ab12 me prod   # assign to that person
forge obs issue assign ISS-ab12 none prod # clear the assignee
```

The server resolves `me` from the **verified credential**, including user-owned CLI
login and helper tokens; the CLI never infers a user from a stored login, decodes a
token, or calls the human-only `GetCurrentUser` RPC. Organization automation tokens
act for nobody: `me` is refused **FailedPrecondition**, exit 2, without a fallback
or retry. Name a user ID instead. `none` on a list sends the API's `unassigned=true`
(not `assignee="none"`); an omitted filter sends neither field, including no false
`unassigned`. Locally every issue is unassigned: `none` includes all rows while a
named assignee or `me` matches none; assignment writes still need a hosted env.

Backend dependency: listing uses the CP #1402 fields. `assign ... me` additionally
requires the owner-approved server-side `AssignIssue.assignee_user_id="me"`
resolution; #1402 alone only supports literal user IDs on that mutation. Until
that backend change lands, use an explicit user ID for assignment.
