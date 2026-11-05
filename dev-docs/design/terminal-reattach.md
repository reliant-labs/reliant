# Terminal reattach: keep the shell across a reconnect

Status: **proposal, needs an owner decision.** Item 4 of the 2026-11-05
terminal incident fixes (`research/2026-11-04-terminal-incident.md`). Items 1
to 3 shipped as code: lossless output (or a loud end), batched frames, and a
wake probe that no longer resets every terminal at once. This one did not,
because it is more than a modest change and has trade-offs the owner should
choose between.

## The problem

Every terminal websocket runs `terminal.create`, which starts a new shell, and
`closeDaemonTerminalSession` kills that shell when the socket ends
(`internal/handlers/terminal/terminal_proxy.go`). So any reconnect (a wake, a
network blip, an api rollout, an output overflow) loses the shell's running
processes, its cwd and environment, and everything printed while the socket
was down. The browser keeps its own xterm scrollback, but the new shell's
prompt lands under it, and a running TUI or build is gone.

Killing on disconnect is deliberate. Before it, every dropped connection
leaked a shell. One daemon held 501, which exhausted macOS's machine-wide PTY
pool, so no program on the machine could open a terminal. Any reattach design
has to keep that leak closed.

## What exists today

- The daemon keeps sessions by id (`terminal.Manager`; `terminal.list`,
  `terminal.close`, `terminal.resize`). It caps them at 64 and evicts the
  least recently active.
- The daemon starts one PTY output pump per session on the first
  terminal-output subscribe (`addIfAbsent`). The pump never pauses, and it
  stops only when the session ends.
- The browser learns the daemon's session id from `init` (`daemonSessionId`
  in `terminalStore`). It keeps the id for the life of the tab, and strips it
  from persisted state, so a reload starts fresh.
- Output chunks carry no sequence number (`TerminalOutputMessage{session_id,
  data}`). Nothing can say which bytes a reconnecting browser already has.

## What reattach needs, whichever option

1. **An attach path.** The websocket takes `sessionId` (plus the pinned
   `daemonId`). The api-server sends a new `terminal.attach` command instead
   of `terminal.create`. The daemon answers `session_gone` when the session no
   longer exists, and the browser falls back to create. A daemon too old to
   know `terminal.attach` answers unknown-command, which is the same fallback.
   Daemons in the field run older versions, so this fallback is required.
2. **Detach instead of kill.** On a disconnect the api-server stops killing
   the shell. The browser's explicit close (the `CloseSession` rpc when a tab
   is closed) still kills it.
3. **A detached-session reaper on the daemon.** This replaces the kill on
   disconnect as the leak guard: a session with no subscriber for longer than
   a TTL is closed. The 64-session cap stays as the backstop. This must land
   before, or together with, step 2.
4. **Pump lifecycle.** While detached, the pump either keeps reading the PTY
   (into a buffer, option B) or stops. Stopping fills the PTY's kernel buffer,
   and the shell's writes then block, freezing a build until the user
   returns. Discarding loses the output.
5. **Browser.** On reconnect, send the session id, keep the xterm buffer, and
   send a resize after attach. SIGWINCH makes full-screen programs repaint.

The api-server holds no state for this, so multi-pod is unaffected: any pod
can attach, because the session lives on the daemon. The session ids are
routed to the user's own machine, the same way `terminal.close` is today.

## Options

### A. Reattach without replay

Steps 1 to 5. While detached, the pump keeps reading and discards (or the
gateway drops it, since no subscriber is registered). After reattach, a
resize repaints TUIs.

- Keeps processes, cwd, env and the browser's scrollback across a reconnect.
- Output printed while detached is lost. A build log has a hole, and a TUI is
  wrong until it repaints. The hole is silent, which is the failure item 1
  just removed from the live stream.
- Size: daemon (attach command, subscriber count, reaper), api-server (attach
  path, no kill on disconnect), browser. No proto change. Roughly 300 lines
  plus tests.

### B. Reattach with replay (recommended)

A, plus offsets and a ring buffer. This is the tmux/mosh model.

- `TerminalOutputMessage` gains `uint64 offset`: the stream position of its
  first byte. The offset runs through the gateway and the api-server to the
  browser (`output` frames carry it).
- The daemon pump writes every chunk into a per-session ring (for example
  1 MiB) **before** sending it, whether or not anyone is attached, and stops
  sending while detached.
- `terminal.attach{session_id, from_offset}` replays from the ring. When
  `from_offset` has already been overwritten, it answers `gap`, and the
  browser clears xterm before the replay (the same as tmux's history limit).
- The result is lossless across a reconnect inside the ring's window, and
  explicit (cleared) beyond it. That matches item 1's rule: lossless, or a
  visible end.
- Size: a proto field, the daemon ring and attach (~250 lines), offset
  plumbing through both hops (~50), the api attach path (~100), browser
  offset tracking (~80), plus tests. Memory is ring × live sessions per
  daemon: 64 MiB at the 64-session cap with a 1 MiB ring.

### C. Keep respawning

Rely on items 1 to 3 to make reconnects rare. Every remaining reconnect still
costs the shell. The cost is nothing now, but the "lost my running process"
reports continue.

## Decisions for the owner

1. **A, B or C.** Recommendation: B. A recreates the silent-hole failure
   across reconnects. B is the coherent end state, and A is most of its cost
   anyway.
2. **Detached TTL.** Suggest 15 minutes: long enough for a laptop lid and a
   network change, short enough that abandoned shells do not pile up. Or
   should a shell survive until the tab is closed, however long that takes?
3. **Reload.** Should a page reload reattach? That needs `daemonSessionId`
   persisted, which it is deliberately not today. Suggest yes, behind the same
   TTL.
4. **Ring size.** Suggest 1 MiB per session.
