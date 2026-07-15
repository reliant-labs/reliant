// Copyright (c) 2025 Reliant Labs
package osutil

// ChildOOMScoreAdj is the value written to /proc/<pid>/oom_score_adj for
// every child process the daemon spawns on behalf of tool executions
// (foreground shell commands, background processes, terminal sessions).
//
// Cloud workspace daemons run inside a k8s pod cgroup shared with dockerd
// and any processes tool calls spawn. When a heavy workload (e.g. a docker
// build) pushes the cgroup over its memory limit, the kernel OOM killer
// picks a victim by badness score — and historically that victim was often
// the daemon itself (exit 137 loops, gateway disconnects, silently broken
// chats). The daemon runs at the default score of 0; raising its children
// to +500 makes the kernel strongly prefer killing the workload over the
// daemon. Raising (never lowering) a score is unprivileged on Linux.
//
// 500 means "treat this process as though it used an extra 50% of total
// memory" — high enough to reliably outrank the daemon, low enough that a
// process using close to nothing doesn't become the victim over a genuine
// memory hog elsewhere in the cgroup. Children inherit the value on fork,
// so grandchildren (the actual build tools a shell spawns) are covered too.
const ChildOOMScoreAdj = 500
