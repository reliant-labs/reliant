---
name: code-search
description: Navigate code efficiently — when a language server answers a question grep cannot, and when grep is the right tool. Use when tracing callers/callees, finding implementations of an interface, or searching an unfamiliar codebase.
compatibility: reliant
metadata:
  category: navigation
  owner: reliant
---
# Code Search

Most code search is text search, and `rg` is the right tool for it. Two questions
are different in kind, because the answer is not present in the text you are
searching. For those, a language server is not a faster grep — it is the only
tool that can be correct.

## The two questions grep cannot answer

**1. "Who calls this / what does this call?"**

A call site does not name the receiver's type. `foo.Execute()` and `bar.Execute()`
are the same eight characters, so a grep for `Execute(` matches every one of them.
In a large Go codebase this can be 85+ distinct methods. The error COMPOUNDS at
every level: a depth-3 grep walk of one method returned functions that were not on
the call path at all, while the language server returned the single true caller.
Depth 1 is usually survivable. Depth 2+ is noise, and chasing that noise costs more
turns than the language server ever will.

**2. "What implements this interface?"**

Implementations do not mention the interface they satisfy. In Go there is no
`implements` keyword to grep for; in TypeScript a structural type may be satisfied
by an object literal that names nothing.

Everything else — finding a definition, listing references to a distinctive name,
reading code — stays with `rg`. It is correct AND one turn, so paying a language
server's startup cost buys nothing.

## What is actually available

Capability varies sharply by language. Check before you rely on it; advice that
fails on contact is worse than no advice.

| Language | How to navigate | call hierarchy | implementations |
|---|---|---|---|
| **Go**, **TypeScript / JavaScript** | the **`code_context` tool** | yes | yes |
| everything else | `rg` | no | no |

**IMPORTANT: for Go and TypeScript, use the `code_context` tool.** One call returns
the definition, its source, and a multi-level call map:

    code_context(symbol: "ResolveDaemon")            # definition + 3-level caller map
    code_context(symbol: "X", want: "callees")       # what it calls
    code_context(symbol: "X", want: "implementations")
    code_context(symbol: "X", file: "svc.go")        # disambiguate a common name

Do not hand-run `gopls` or `tsserver` — code_context drives them, scopes results to
your workspace (no stdlib noise), and traces depth without a turn per hop.

**IMPORTANT: never walk callers with `rg`.** A call site does not name its
receiver's type, so `rg` matches every same-named method and the error compounds at
every level — a measured depth-3 grep walk returned functions that were not on the
call path at all. `rg` remains correct for name lookup, full-text search, and for
languages with no server.

## Traps

**`rg -r` is REPLACE, not recursive.** `rg -rn 'RenderProjectMemory' .` silently
rewrites matched text in the output — it printed `forgecli.n(projectPath)` for a
line whose real content was `forgecli.RenderProjectMemory(projectPath)`. It exits
0 and looks like a normal result. `rg` recurses by default; you never need a flag
for it. This produces wrong answers that look right, which is worse than an error.

**Read the output you already have.** Before running a second search to interpret
the first, re-read the first. A naive `rg` for a type name returned 148 lines that
already contained the four-file pattern that a follow-up "smarter" query was run to
discover.

**Scope every search to a relative path.** `rg pattern internal/`, never `rg pattern /`
or a home directory — an unbounded search reads unrelated worktrees and vendored
trees, and can exhaust the output budget on a single call.

**Chain independent probes into one command.** A turn costs a full model
generation; a shell command costs milliseconds. Separate probes with `;` and label
each with an echo. Chaining also expresses DEPENDENT probes, which parallel tool
calls cannot:

    f=$(rg -l 'ListWidgets' proto/); echo "$f"; grep -n 'ListWidgets' -A 12 "$f"
