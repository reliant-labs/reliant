---
name: deploy/config-check
description: The boot config check `forge env build --push` / `--release` / `forge env deploy` run before pushing — each workload booted with its rendered env under FORGE_CONFIG_CHECK=1, what counts as set, where cross-field rules belong, and the --config-check=gate|warn|off rollout.
---

# The config check runs before the push

Between building the project image and pushing it, `--push` / `--release`
(and so `forge env deploy`) run every workload of that image the way the env
runs it — its command, args and rendered env — in a `docker run --network
none` with `FORGE_CONFIG_CHECK=1`. The binary's typed config load
(`forge/pkg/config`) then reports every problem it would boot with (a
required variable the env never wires, a value outside `allowed_values`, a
`buf.validate` field or CEL rule the values break) and exits. A variable the
env wires from a Secret, the secret store, a database credential, a
ConfigMap or the Downward API gets a placeholder: it counts as set, and its
value is not judged. A failure refuses the push and names the workload; so
does a workload that prints no result (it exits, or blocks for 30s, before
its config load), since its config was never verified.

## Cross-field rules belong on the config message

Declare a cross-field requirement as a CEL rule on the config message
(`option (buf.validate.message).cel`), not in a constructor: a constructor
runs after the check has exited, and the env that breaks it ships.

## Rollout and opting out

It gates from v1.0.16; a project pinned below that sees the failures as
warnings. `--config-check=gate` opts in now, `=warn` keeps pushing, `=off`
skips it. Workloads it does not run (another image, host runtime, a binary
built before the check) are listed with the reason.
