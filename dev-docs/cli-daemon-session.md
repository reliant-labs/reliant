# Explicit daemon session for project and trigger CLI commands

`reliant project … --daemon-session` and `reliant trigger … --daemon-session`
use the existing signed-in daemon credential selected for that exact server by
`auth.ReadDaemonCredentials`. They do not mint or widen authority, copy the
credential to another store, or change the normal CLI login default.

This is useful when a workflow operator runs on an already authenticated daemon
but has no separate `reliant auth login` entry. For example:

```sh
reliant project list --daemon-session --json
reliant trigger list --daemon-session --project-id <project-id>
reliant trigger fire --daemon-session <trigger-id>
reliant trigger events --daemon-session <trigger-id>
```

The server is still chosen by `--server`, environment, then the build default.
There is no cross-server fallback, expired or ambiguous credentials fail closed,
and combining the explicit flag with `RELIANT_TOKEN` is rejected. Legacy
single-account records without a `Sub` hint remain usable: the authenticated API
establishes their acting user, never the local hint. Credential diagnostics name
only `daemon-session (server-bound signed-in account)`, not the bearer value.

This flag is authentication, not permission. All server authorization, owner
checks, existing agent tool restrictions, and unattended execution restrictions
remain unchanged. It does not make trigger creation, enablement, or firing an
agent tool where that tool was not already permitted.
