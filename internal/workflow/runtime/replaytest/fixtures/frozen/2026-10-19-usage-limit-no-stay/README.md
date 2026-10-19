# Frozen: a stay on a fallback model ends at its first recheck (recorded before usage-limit-stay)

`usage_limit_wait.json` at the commit before `usage-limit-stay`. The spawned
agent's first `CallLLM` hits a provider usage limit, writes its wait marker
(`UsageLimitWait`), parks on a durable timer, clears the marker, retries the
step (`usage-limit-wait` and `usage-limit-failover` version markers) and
completes.

After the change the same failure also consults `workflow.GetVersion
("usage-limit-stay")`, which governs two refinements: a probe bench is a
recheck time and not a reset (`UsageLimitFacts.ProviderResetIsProbe`), and a
fallback stay survives a refused attempt to return instead of lapsing at its
first end. A history without that marker takes the branch it recorded: it
ignores the probe flag and a stay lapses at its end, as before.

What keeps this replaying: `usageLimitStayChangeID` (`usage-limit-stay`,
`internal/workflow/runtime/usage_limit_wait.go`), consulted in
`usageLimitWaiter.wait` right after `usage-limit-failover`. The gate adds no
command of its own; it only decides how the facts and the lease are read.

Only this fixture is frozen: no other shape in the set reaches a usage limit.

Retire this set only together with the gate, once no run that waited on a usage
limit before the gate's deploy can still be open.
