# Frozen: a usage limit waits for its own reset (recorded before account failover)

`usage_limit_wait.json` at the commit before usage-limit failover. The spawned
agent's first `CallLLM` hits a provider usage limit, writes its wait marker
(`UsageLimitWait`), parks on a durable timer until that limit's reset plus the
margin, clears the marker, retries the step (`usage-limit-wait` version marker)
and completes.

After the change, the same failure also consults `workflow.GetVersion
("usage-limit-failover")` and may act on the facts the activity attached
(another account of the provider: retry at once; an opted-in cross-provider
fallback: a lease on another model). A history without that marker takes the
branch it recorded and waits, whatever facts the activity now reports.

What keeps this replaying: `usageLimitFailoverChangeID`
(`usage-limit-failover`, `internal/workflow/runtime/usage_limit_wait.go`),
consulted in `usageLimitWaiter.wait` right after `usage-limit-wait`.

Only this fixture is frozen: no other shape in the set reaches a usage limit,
and the new gate is consulted nowhere else.

Retire this set only together with the gate, once no run that waited on a usage
limit before the gate's deploy can still be open.
