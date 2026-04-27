---
name: documentation-writing
description: Documentation methodology — narrative-driven technical writing, teaching vs reference distinction, and investigation-based accuracy
---

# Documentation Writing

## The Documentation Mindset

Great documentation feels like a conversation with a knowledgeable colleague who understands what you're trying to accomplish. It anticipates questions, explains the reasoning behind design decisions, and guides readers through complexity with patience and clarity.

The difference between mediocre and excellent documentation isn't comprehensiveness—it's whether readers finish with genuine understanding or just a collection of facts they'll need to look up again. Your goal is always the former.

## Teaching vs Reference: The Core Distinction

Documentation serves two fundamentally different purposes, and confusing them is the source of most documentation problems.

**Teaching content** helps readers understand concepts, build mental models, and learn how things work together. This content should be written as prose—flowing paragraphs that explain, contextualize, and connect ideas. Teaching content answers "what is this?", "why does it work this way?", and "how do these pieces fit together?"

**Reference content** helps readers look up specific details they already understand conceptually. This content works well as lists, tables, and structured formats optimized for scanning. Reference content answers "what are the parameters?", "what's the exact syntax?", and "what are all the options?"

The critical mistake is using reference formatting (bullets, sparse lists) for teaching content. When you write:

```markdown
## Features
- Multi-agent architecture
- Session management  
- File tracking
- LSP integration
```

You've created something that's neither good teaching (no explanation) nor good reference (no details). The reader learns nothing and can't look anything up. This format exists because it's easy to write, not because it serves readers.

Compare that to teaching content that actually teaches:

```markdown
## How Reliant Works

Reliant divides complex development tasks among specialized agents, each focused on what it does best. When you ask Reliant to implement a feature, a Research Agent first explores your codebase to understand its patterns, conventions, and architecture. Only after building this context does the Implementation Agent begin writing code—and because it has the research context, it writes code that fits naturally with your existing codebase rather than generic solutions.

Every interaction happens within a session that Reliant automatically saves to a local database. You can close your terminal, restart your computer, and pick up exactly where you left off days later. The session preserves not just the conversation, but the full context: which files were examined, what changes were made, and what the agents learned about your codebase.

When agents modify files, Reliant tracks every change with before-and-after diffs. This isn't just for review—it integrates with your version control workflow, making it easy to see exactly what changed in a session and roll back specific modifications if needed.
```

Notice how each concept flows into the next, building understanding progressively. A reader finishes this with a mental model of how the system works, not a list of terms to research.

## When Lists and Tables Are Appropriate

Lists and tables excel at reference content where readers need to scan for specific information. Use them for:

**Configuration options and parameters**, where readers know what they're looking for and need exact syntax:

```markdown
## Configuration Options

The agent configuration file accepts these settings:

| Setting | Type | Default | Description |
|---------|------|---------|-------------|
| `temperature` | float | 1.0 | Controls response randomness. Lower values (0.1-0.3) produce focused, deterministic output. Higher values (0.7-1.0) increase creativity and variation. |
| `max_tokens` | integer | 4096 | Maximum length of generated responses. Increase for tasks requiring long-form output like documentation or detailed analysis. |
| `tools` | array | [] | List of tool names this agent can access. See the Tool Reference for available tools and their capabilities. |
```

**Command references and flags**, where readers need to quickly find syntax:

```markdown
## Command Line Options

| Flag | Description |
|------|-------------|
| `--verbose`, `-v` | Enable detailed logging output, useful for debugging agent behavior or understanding tool execution |
| `--session <id>` | Resume an existing session instead of starting fresh. Find session IDs with `reliant sessions list` |
| `--worktree <name>` | Run in an isolated git worktree, keeping experimental changes separate from your main branch |
```

**Step-by-step procedures** where order matters and steps are genuinely discrete:

```markdown
## Installing Reliant

1. Download the installer for your platform from the releases page. The macOS version requires macOS 12 or later; the Linux version supports most distributions with glibc 2.31+.

2. Run the installer, which places the `reliant` binary in `/usr/local/bin` and creates a configuration directory at `~/.reliant`. The installer doesn't require root access—if you lack write permission to `/usr/local/bin`, it offers to install to `~/.local/bin` instead.

3. Verify the installation by running `reliant --version`. You should see version information and a note about available updates if any exist.

4. Run `reliant setup` to configure your API keys and preferences. The setup wizard walks through essential configuration and validates that everything works correctly.
```

Notice that even numbered steps have substantial explanations—they're not terse commands but guided instructions that anticipate questions.

## Writing Effective Explanations

When explaining a concept, feature, or system, follow this structure naturally (not as a rigid template):

**Start with what it is and why it matters.** Don't assume readers know why they should care. A feature explanation that opens with "The session replay system allows you to..." tells readers nothing about whether they should keep reading. Instead: "When debugging agent behavior or testing prompt changes, you need to run the exact same conversation multiple times with different configurations. Session replay captures conversations and lets you re-run them against different models or prompts..."

**Explain how it works at the level readers need.** Not implementation details (unless documenting internals), but the mental model readers need to use it effectively.

**Show realistic examples.** Not minimal toy examples, but scenarios readers actually encounter.

**Connect to related concepts.** Documentation shouldn't be isolated islands. After explaining a feature, mention what readers might explore next.

## Cross-Linking Strategy

Cross-linking serves two purposes: it helps readers discover related content, and it lets you write focused documents without repeating information.

**Link when you mention something that has its own documentation.** If you reference "worktrees" while explaining a different feature, link to the worktree documentation. But don't just drop a link—give readers enough context to decide whether to follow it.

**Create bidirectional links.** If Document A links to Document B, add a "Related Topics" section to Document B linking back to A.

**Write link text that describes the destination.** Not "see [here](./auth.md)" but "for manual refresh or custom token lifetimes, see the [Authentication Guide's token management section](./auth.md#token-management)."

**Don't duplicate, reference.** When the same information is relevant in multiple places, write it once and link to it from elsewhere. Duplication creates maintenance burden and inevitably leads to inconsistency.

## Writing Process

**Investigate thoroughly before writing.** Read the source code, understand how things actually work, verify your assumptions. Documentation based on assumptions rather than investigation is documentation that misleads readers.

**Test every example.** Run code samples, verify command outputs, confirm that configuration options actually exist. Untested examples are often wrong, and wrong examples frustrate readers more than missing examples.

**Write for someone unfamiliar.** You understand the system deeply after investigating it—your readers don't. Explain terms when you introduce them, state assumptions explicitly, provide context that seems obvious to you but won't be obvious to someone encountering this for the first time.

**Read your own documentation.** After writing, read through as if you were a developer encountering this system for the first time. Are the explanations clear? Do the examples make sense? Can you navigate to related information easily? Would you enjoy reading this, or would you skim it looking for code to copy?

## What to Avoid

**Don't write bullet lists of features or concepts.** If you find yourself writing a list of 5-10 short items without substantial explanation, stop and rewrite as prose. Those bullets might be useful as a summary after explanation, but they can't replace explanation.

**Don't write minimal examples.** Examples that show only syntax without context don't help readers apply knowledge to their real situations. Show realistic scenarios with explanation of what's happening and why.

**Don't assume readers will follow every link.** Each document should be useful on its own, with links providing depth rather than essential information.

**Don't document the obvious.** If the CLI help text or function signature fully explains something, don't restate it. Documentation adds value by providing context, explaining why, showing examples, and connecting concepts—not by restating what readers can see in the code.

**Don't fragment explanations across many small sections.** A feature explanation split into "Overview", "Details", "Usage", "Examples", and "Notes" sections forces readers to jump around to build understanding. A cohesive explanation that flows naturally serves readers better.

## Tools and Workflow

Your tools support thorough investigation and careful writing. Use them accordingly.

**Investigation phase**: Use `glob` and `ls` to understand project structure. Use `grep` to find patterns, implementations, and usage examples. Use `view` to read source files and understand how things actually work. Spend significant time here—thorough understanding produces accurate, insightful documentation.

**Writing phase**: Use `write` to create new documentation and `edit` to refine it. Work incrementally, starting with structure and filling in sections. Review and revise—first drafts rarely achieve the clarity readers deserve.

**Verification phase**: Use the shell tool (`bash`/`powershell`) to test code examples and verify commands. Use `view` to confirm file paths and function signatures. Use `fetch` to check external links and gather additional context from external resources when appropriate.

The extra time spent on investigation and verification pays off in documentation that readers trust because it's accurate, and value because it's insightful.
