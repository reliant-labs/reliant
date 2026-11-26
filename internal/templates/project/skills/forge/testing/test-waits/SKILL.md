---
name: test-waits
description: "The test-waits lint lane — forge-test-fixed-sleep, forge-test-tight-deadline, forge-test-wall-clock-bound: what each flags, the 5s floor, and how to write a negative assertion, a fixed-duration workload or a latency-bound test so the rule agrees."
---

# Test waits: the lint rules and their escapes

`forge lint` flags the test shapes that flake on a loaded machine (the
`test-waits` lane, also `forge lint --test-waits` alone), in `_test.go` files
only. The fix it points to is `testkit.Eventually` / `testkit.Receive`
(`forge/pkg/testkit`; see the parent `testing` skill).

| Rule | Flags | Does not flag |
|---|---|---|
| `forge-test-fixed-sleep` | `time.Sleep(d)` or a bare `<-time.After(d)` in a test body (a `*testing.T` / `testing.TB` func, a `t.Run` literal, a suite's `Test*` method) with more of the test after it | a sleep in a loop (a poll — the remedy), in a goroutine, fake or handler, as the last statement, right before a context `cancel()`, of at most 2ms (a timestamp tick), in a helper process that calls `os.Exit`, or inside `synctest` |
| `forge-test-tight-deadline` | an event wait that FAILS the test after a constant deadline under 5s: a `select` case on `time.After` / a timer / a `context.WithTimeout` ctx whose body calls `t.Fatal`/`t.Error`/`Fail`; testify `Eventually`; a poll bounded by `time.Now().Add(d)`, `time.Since(start) < d`, or a counted loop of sleeps that can exit early | a negative assertion (expiry is the pass), a deadline of 5s or more, a duration the file does not spell as a constant, a clock-bounded loop that cannot end before its deadline (a fixed-duration workload: no `break`/`return` outside the deadline's own check, no other term in its condition) |
| `forge-test-wall-clock-bound` | a test that fails when real elapsed time passes a constant under 5s: `if time.Since(start) > d { t.Fatal }`, `assert.Less(t, elapsed, d)`, `assert.WithinDuration(t, time.Now(), x, d)` | a lower bound (`elapsed < backoff` → fail: load cannot make it fail) |

The floor is **5s** for all three (`LoadTolerantDeadline`): a constant deadline
or elapsed-time upper bound of 5s or more is never flagged, and a duration the
file does not spell as a constant (read from config, computed) is not judged.
An elapsed-time bound is `time.Since(x)` / `time.Now().Sub(x)` (or a variable
holding one) compared with a constant by `<` `<=` `>` `>=`, either way round.

The rules warn in a project pinned below the release after the one that ships
them, then gate.

## Deliberate shapes, written so the rule agrees

- **"Nothing happens" (a negative assertion).** Write it as a `select` whose
  timer case is the pass. It is not flagged at any duration, and load only
  makes it pass more easily:

  ```go
  select {
  case ev := <-events:
      t.Fatalf("unexpected event while the server was down: %v", ev)
  case <-time.After(150 * time.Millisecond): // three heartbeat intervals of silence
  }
  ```

  A bare `time.Sleep` followed by a check of state is the flagged shape.
- **A fixed amount of work.** A loop that runs until its clock runs out and
  can end no other way — `for time.Now().Before(dl) { heartbeat();
  time.Sleep(50 * time.Millisecond) }` — is a workload, not a wait, and is not
  flagged. Prefer a counted loop (`for range 60`) when the number of rounds
  matters more than the wall time.
- **A test whose purpose IS a latency bound** ("Flush returns instead of
  hanging behind a hung call"). Bound it at a timeout a correct implementation
  beats on any machine — 5s or more. When the tight bound itself is the
  requirement (an SLO), suppress it at its line with the reason, and gate the
  test behind `testkit.Slow` so a loaded `-short` run does not carry it.

## Suppressing one

A deliberate wait the shapes above cannot express is suppressed at its line
with the reason, like any forge rule:

```go
// forge:lint-disable-next-line forge-test-fixed-sleep: asserts nothing is sent within 100ms
time.Sleep(100 * time.Millisecond)
```
