// Package testcollect pulls Go coverage from an env's running instrumented
// servers into a test run: what `forge test collect <env>` does, and what
// `forge env down` and `forge env preview delete` do before they tear an env
// down.
//
// A server built with `-cover -covermode=atomic` (an instrumented env's Go
// workloads, see internal/cli/build_cover.go) has coverage in two places:
//
//   - live, in the process. serverkit serves it at /debug/coverage on the
//     loopback debug listener (pkg/serverkit/coverage.go): a tar of the
//     covmeta and covcounters files Go would write. The endpoint is
//     read-only: nothing a request sends changes the counters.
//   - flushed, in GOCOVERDIR. Go writes the counters there when main returns
//     and on os.Exit, so every process that exited cleanly left a file.
//
// Each runtime reaches them its own way:
//
//	host     GET http://127.0.0.1:<pprof port>/debug/coverage, plus the
//	         process's GOCOVERDIR, .forge/tests/covdata/<env>/<workload>/
//	compose  `docker cp <container>:<GOCOVERDIR>/. -`: the scaffolded compose
//	         binds pprof on 0.0.0.0, so serverkit withholds the endpoint, and
//	         a copy needs nothing in the image (a distroless cover stage has
//	         no tar)
//	cluster  a port-forward stream to each running pod's debug port
//	         (pkg/dbquery/kubedial), then the same GET
//
// Files are grouped by their covmeta hash, which names one binary build. Under
// air every rebuild is a new hash, so a workload's GOCOVERDIR can hold several
// builds: the newest is the workload's coverage, and each older one is kept
// beside it, labelled "stale build", and never merged with it (amendment 5:
// Go's own `covdata merge` sums two builds of one package into wrong numbers).
// Different workloads are different binaries with different hashes even from
// one tree; they are kept apart here and merged by pkg/testreport, which
// refuses only different source.
//
// The run layout is pkg/testreport's: the newest build of each workload goes
// in <run>/<lane>/covdata/<workload>/, listed in the lane's capture.json as a
// go-covdata input, and the stale ones in <run>/<lane>/covdata/<workload>/stale/<hash>/,
// listed nowhere, so no reader merges them.
//
// `--reset` starts a run from zero on the client side: it pulls each live
// process's counters and records them as that process's baseline, which every
// later collect subtracts (baseline.go), and records the counter files flushed
// before it, by source, name and content digest, which every later collect
// leaves out (fence.go). Both are one record, .forge/tests/covdata/<env>/.reset/<workload>,
// published atomically (reset.go). When a baseline applied, or one process
// left several snapshots, the lane's input is the build's profile combined
// process by process (CombinedProfile) instead of its raw covdata, which is
// kept beside it.
//
// Collection is best effort per workload: one that cannot be reached is
// reported, never fatal to the others.
package testcollect
