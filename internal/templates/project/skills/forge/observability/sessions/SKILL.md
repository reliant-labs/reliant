---
name: observability/sessions
description: Investigate what one user did — forge obs sessions to find the sessions that raised errors, forge obs session for the timeline (navigation, clicks, failed requests, errors), then the issue, trace and logs its ids lead to. What the timeline never holds, and its JSON shapes.
---

# Investigate a user session

"A user hit a bug" is a reproduction, not a stack trace. The session timeline is what they did, in order.

```bash
forge obs sessions prod --has-error --since 24h          # which sessions raised errors (add --user, --path /checkout)
forge obs session 91b955b313a1a86644ea53730a92deab prod # the timeline
```

```
+00:12.4  click     button //*[@id="save"] on /jobs/new
+00:12.5  request   POST /api.v1.JobService/CreateJob → 500 (230ms)  trace 0af7651916cd43dd8448eb211c80319c
+00:12.6  error     TypeError: Cannot read properties of undefined on /jobs/new → issue ISS-ab12  trace 0af7…
```

Read it top to bottom; the steps before the failure are the repro steps for the fix PR. Then follow the ids, which `suggested:` names:

1. `forge obs issue ISS-ab12 prod`: the error entry's `→ issue <id>`: culprit, frames, suspect commits. An error with a fingerprint and no issue id has not been folded into an issue yet.
2. `forge obs trace <trace-id> prod`: the server side of a failed request: which span failed and why.
3. `forge obs logs prod --trace <trace-id>`: the log lines of that request.

What the timeline never holds: element text, query strings, fragments and input values (paths are cut, clicks are a tag and an xpath, the recorder masks text). A session with `has_replay` has a recording; watch it in the console. Sessions come from the control plane's browser tables, so a local env exits 2 and says so. Exit 1: a malformed id, or a session the env does not have (or older than 30 days). `--json` is `forge.obs.sessions/v1` and `forge.obs.session/v1`; each timeline entry has `offset_ms` since the session started, `kind` (`navigation`, `click`, `request_failed`, `error`, `event`), `trace_id`, `issue_id`.
