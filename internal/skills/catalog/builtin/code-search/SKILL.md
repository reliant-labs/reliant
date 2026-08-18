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

| Language | Tool | call hierarchy | implementations | references | hover (inferred types) |
|---|---|---|---|---|---|
| **Go** | `gopls` CLI, and `gopls mcp` if your harness exposes it | yes | yes | yes | yes |
| **TypeScript / C#** | `mcp-language-server` bridge, when enabled | **no** | **no** | yes | yes |
| **Rust / C++** | `rust-analyzer`, `clangd` | no CLI query surface — LSP/MCP only | | | |

For TypeScript and C#, `hover` is the unique win: it returns the INFERRED type of
`const x = useFoo()` with no annotation, which `rg` cannot compute at any cost.
Call hierarchy is not available through the bridge, so for those languages a
careful `rg` remains the practical approach for caller walks — keep the depth
shallow and verify.

Probe first; degrade without ceremony:

    command -v gopls >/dev/null && gopls call_hierarchy path/to/file.go:LINE:COL

## Go: the commands

    gopls call_hierarchy path/to/file.go:LINE:COL   # callers AND callees
    gopls implementation path/to/file.go:LINE:COL   # interface -> concrete types
    gopls references     path/to/file.go:LINE:COL   # only when rg is ambiguous

Get LINE:COL from an `rg -n` hit first (the column is the 1-based offset of the
identifier on that line). CHAIN the lookup and the query into ONE command rather
than spending a turn on each:

    f=internal/svc/x.go; l=$(rg -n 'func .*executeApproval' $f | cut -d: -f1); \
      gopls call_hierarchy $f:$l:34

A cold `gopls` CLI invocation measured ~9s in a large repo. That is why it is worth
it for the two questions above and a waste for a plain name lookup. An always-warm
MCP server avoids the startup cost; prefer it when your harness exposes one.

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
