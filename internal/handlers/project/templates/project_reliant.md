# Project Instructions

<!-- These instructions are loaded into every agent conversation in this project. -->
<!-- Edit this file to customize how agents work with your codebase. -->

## Guidelines

- 

## Key Context

- 

## Code navigation

Two questions are not text search, because the answer is not in the text: "who
calls this / what does this call" and "what implements this interface". A call
site does not name the receiver's type, and an implementation does not mention
its interface, so grep matches every same-named symbol and the error compounds
at each level.

IMPORTANT: in Go and TypeScript, use the `code_context` tool for those —
`code_context(symbol: "X")` returns the definition, its source and a
multi-level call map in one call. Everything else — definitions, references to a
distinctive name, reading code, and any other language — is faster with
`rg`. Load the `code-search` skill for the details.

## Available Skills

Use `skill list` to see all available skills. Key skills:
- `code-search`: Navigate code efficiently (when a language server beats grep, and when it doesn't)
- `reliant-config`: Configure Reliant (memory, skills, MCP, presets, creating skills)
- `workflow-builder`: Build agents and automations that run on a schedule or an event, and multi-agent workflows
