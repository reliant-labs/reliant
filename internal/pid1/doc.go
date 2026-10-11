// Copyright (c) 2025 Reliant Labs

// Package pid1 makes reliant a correct init process when it is started as a
// PID namespace's PID 1.
//
// The kernel reparents every orphaned process in a PID namespace to that
// namespace's PID 1 and relies on PID 1 to wait(2) for it. A PID 1 that never
// waits leaks a zombie — a task-table slot — per orphan, forever. reliant is
// PID 1 wherever a container runs it directly: every workspace pod (the
// controller's command is `reliant daemon start`, and the docker tier's
// entrypoint execs it), the reliant image (ENTRYPOINT ["reliant"]), and any
// `docker run` of either. Its tools orphan processes all day — a shell that
// exits before its background job, a killed process group, a daemonizing
// tool — so a workspace collected ~1,400 zombies a day, on course for
// kernel.threads-max, after which fork() fails and nothing can run.
//
// Reaping in-process is not an option. A wait4(-1) loop in the daemon would
// race os/exec: whichever wait runs first collects the status, so an
// exec.Cmd.Wait that loses fails with ECHILD and its caller sees the wrong
// exit code. Ignoring SIGCHLD has the same effect for every child at once.
//
// So Init splits the process in two, the way tini does, without depending on
// the image to ship tini: PID 1 re-executes its own binary as a child and
// becomes a minimal init for it. The init reaps every child the kernel hands
// it, forwards termination signals to the child, and exits with the child's
// status. The child — the real program — is an ordinary process: none of its
// own children are ever waited for by anyone else.
//
// Init is a no-op in every process that is not PID 1 (a desktop daemon under
// Electron, a CLI in a terminal, a server under systemd): there, the system's
// own init or subreaper already reaps orphans.
package pid1
