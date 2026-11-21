## review:changes

Lane: **MEDIUM** (one pass on gpt-6-luna@codex).

MEDIUM: code (cmd/reliant/cmd/daemon.go, cmd/reliant/cmd/daemon_instance.go, …); dependencies (go.mod)

The earlier data-directory handoff race is fixed: `daemon start` holds its claim through runtime startup. The new terminal token prompt, however, does not disable terminal echo, so pasted credentials remain visible despite the stated security fix.

- **blocker** [B1] `cmd/reliant/cmd/daemon.go:602` — The code calls `term.GetState` but never changes terminal mode before starting `term.ReadPassword`. `ReadPassword` reads from the fd but does not itself turn echo off; the saved state is only restored later. Consequently the pasted PAT is still echoed to the terminal and can be exposed in scrollback or session recordings, defeating the credential confidentiality this change intends to provide. Fix: Disable echo before launching the read goroutine (for example, use x/term's password-reading API that handles terminal mode, or set a no-echo state explicitly), and retain deferred restoration on every return path. Add a test of the terminal mode transition/restoration.

**Earlier blockers, at this head:**
- R1.B1 fixed — cmd/reliant/cmd/daemon.go:1034-1038 holds the claim and passes it to Start; internal/toolexec/daemonruntime/runtime.go:344-364 uses the caller's existing claim.

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `dac51b3da1a83ef31040cb1d8eb0e1c28c7e60e5`.
<!-- review-changes sha=dac51b3da1a83ef31040cb1d8eb0e1c28c7e60e5 -->
<!-- review-round sha=dac51b3da1a83ef31040cb1d8eb0e1c28c7e60e5 kind=code -->
<!-- review-blockers sha=dac51b3da1a83ef31040cb1d8eb0e1c28c7e60e5 [{"gap":"defect","kind":"code","line":602,"path":"cmd/reliant/cmd/daemon.go","problem":"The code calls `term.GetState` but never changes terminal mode before starting `term.ReadPassword`. `ReadPassword` reads from the fd but does not itself turn echo off; the saved state is only restored later. Consequently the pasted PAT is still echoed to the terminal and can be exposed in scrollback or session recordings, defeating the credential confidentiality this change intends to provide."}] -->