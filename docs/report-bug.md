# Reporting a forge bug

**forge sends nothing unless someone runs `forge report-bug`.** There is no automatic crash
reporting, no usage telemetry and no background sending. Nothing on this page happens
on its own.

## Filing a report

```bash
forge report-bug --severity high \
  --title "forge generate drops optional fields from list filters" \
  --actual "ListTasksRequest.status is missing from the generated filter" \
  --evidence "forge generate on proto/services/tasks/v1/tasks.proto"
```

or the same fields as one JSON object on stdin:

```bash
echo '{"severity":"low","title":"typo in forge lint --help","actual":"receive"}' \
  | forge report-bug --json
```

| field      | required          | what goes in it                                                                                                                  |
| ---------- | ----------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `severity` | yes               | `critical` (data loss, or nothing can proceed), `high` (it blocks your task), `medium` (there is a workaround), `low` (cosmetic) |
| `title`    | yes               | one line naming the defect; ids and numbers go in `evidence`                                                                     |
| `summary`  | one of these four | what happened                                                                                                                    |
| `expected` |                   | what should have happened                                                                                                        |
| `actual`   |                   | what happened instead, with the exact error text                                                                                 |
| `evidence` |                   | how to reproduce it: commands, output, file paths                                                                                |

It prints the report's id. Use it for defects in forge itself — a forge command that
fails, hangs or generates something wrong, or a lint, skill or doc that is wrong — not
for bugs in your own code, your failing tests, or an agent's own mistakes.

- **Repeats are not resent.** The same title (compared case- and punctuation-blind,
  ignoring words that hold digits) within 30 minutes prints the first report's id and
  sends nothing.
- **`--dry-run`** prints the exact request body and sends nothing.
- **`--anonymous`** leaves off your forge login even when you are logged in.
- **`-o json`** prints `{"report_id": ..., "duplicate": ..., "filed_at": ...}`.
- The exit status is non-zero when the report could not be filed (invalid fields, no
  network, or the ingest's rate limit); nothing is retried in the background.

## Exactly what is sent

One HTTPS request, `POST https://admin.reliantapi.com/controlplane.v1.ForgeReportService/FileBugReport`
(`FORGE_REPORT_ENDPOINT` overrides the origin), with this JSON body:

| field                                                            | value                                                                                                                                                                                                                           |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `reportId`                                                       | a random UUID minted for this report                                                                                                                                                                                            |
| `severity`, `title`, `summary`, `expected`, `actual`, `evidence` | what you wrote, redacted (below); `title` limited to 200 UTF-8 bytes, the other text fields to 4000, including any truncation marker; cuts preserve whole characters                                                            |
| `client.forgeVersion`                                            | this forge's version                                                                                                                                                                                                            |
| `client.os`, `client.arch`, `client.goVersion`                   | the operating system, CPU architecture and Go runtime forge was built with                                                                                                                                                      |
| `client.commandLine`                                             | the command line, redacted (below)                                                                                                                                                                                              |
| `client.projectForgeVersion`                                     | `forge_version` from the `forge.yaml` of the project you ran it in, if any                                                                                                                                                      |
| `client.harness`                                                 | the agent harness forge detected (`reliant`, `claude`, `codex`, `cursor`), empty for a person at a terminal. Detected from whether the harness's marker variable is set; the variable's value is not sent                       |
| `client.installId`                                               | a random UUID minted the first time you file a report and kept in `report-bug.json` in forge's config directory; the ingest rate-limits by it. It is not derived from your machine or account; delete the file to get a new one |

When you are logged in (`forge login`) and do not pass `--anonymous`, the request also
carries your login as a bearer token, and the report records your user and organization
id so the team can follow up. A token the server cannot validate is ignored.

The response is empty: the endpoint returns nothing about any report.

### What is never sent

- **File contents.** Nothing is read from your project except `forge_version` in
  `forge.yaml`. A report contains file contents only if you paste them into a field —
  don't.
- **Environment variable values.** The harness is detected from which marker variable
  is set; no value is read into the report.
- **Tokens and credentials.** Every text field and the command line pass through
  `forge/pkg/redact`, which replaces credential shapes with `[redacted]`: URL userinfo
  (`postgres://user:pass@`), `key=value` pairs whose key names a credential
  (`password`, `token`, `secret`, `api_key`, `authorization`, ...), bearer and basic
  schemes, JWTs, the key prefixes of common providers (Anthropic, OpenAI, Stripe,
  GitHub, GitLab, Slack, Google, AWS) and long opaque runs of 40+ characters.
- **Your home directory.** `/home/<you>`, `/Users/<you>` and `C:\Users\<you>` become
  `~`.
- **report-bug's own text, twice.** In the command line the values of `--title`,
  `--summary`, `--expected`, `--actual` and `--evidence` become `…`, and the value of
  any flag whose name looks like a credential (`--token`, `--password`, `--api-key`,
  `--dsn`, ...) becomes `[redacted]`. The program path becomes its base name.

Redaction is pattern-based: it catches the shapes credentials are usually written in,
and the server applies it again, but it cannot recognise a secret that looks like
ordinary text. Read `--dry-run` when in doubt.

## Where reports go

The control plane's ingest writes each report as one issue record in Reliant's own
observability (source `forge_cli_report`), where the forge team triages it. Reports are
never published to a public tracker. The ingest limits how many reports one install,
one network and the whole service accept per hour.
