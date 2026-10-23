---
name: research-methodology
description: Comprehensive investigation methodology for codebase analysis, system architecture discovery, and technical research
---

# Research Methodology

## Phase 1: Scope the investigation

**Requirements**: State what you're researching and why — the specific questions to answer, the decisions that ride on the findings, and the level of detail required.

**Prioritization**: Identify the areas with the greatest impact on subsequent planning and implementation first. Favor high-value targets that yield maximum insight for minimal effort.

**Strategy**: Decide which tools you'll use, which files and directories to examine first, and how you'll organize findings — before you start reading. A planned investigation beats random exploration.

## Phase 2: System architecture and codebase

**Architectural overview**: Map the major components, how they interact, and the technologies and frameworks in play before diving into detail.

**Technology stack**: Identify languages, frameworks, and tools, with version constraints, compatibility requirements, and upgrade paths. Record the rationale behind technology choices where the code or docs make it evident.

**Component mapping**: Use glob/ls/view to learn how the codebase is organized — major modules, their responsibilities, their relationships, and the patterns in directory structure, file naming, and code organization.

**Dependencies**: Map internal (component-to-component) and external (third-party libraries, APIs, databases, services) dependencies, including versions, update policy, and security/compatibility exposure.

## Phase 3: Patterns and conventions

**Code patterns**: Grep for recurring solutions — design patterns, error-handling style, naming conventions, and how similar problems are solved consistently.

**APIs and interfaces**: How components communicate, how data contracts are defined and maintained, and what authentication/authorization patterns are used.

**Configuration**: What config files exist, how environment-specific settings are managed across environments, and what the deployment patterns require.

**Testing**: Frameworks in use, coverage levels, and how unit / integration / end-to-end tests are organized and executed.

## Phase 4: History and evolution

**Change history**: Use git history to see how the system evolved — patterns of change, primary contributors, and the areas that churn most.

**Known problems**: TODO comments, tracked issues, technical debt, and code that looks incomplete or problematic. What has the team struggled with, and how did they respond?

**Performance and scale**: Performance-critical paths, the most expensive operations, known bottlenecks, how performance is monitored and optimized, and how the system behaves under load. Note resource-usage patterns (memory, CPU, disk, network) that signal scaling limits, plus existing caching and optimization strategies.

## Phase 5: Requirements, context, and integrations

**Requirements and context**: Read existing documentation, issue trackers, and user stories to learn what the system is supposed to do, and look for gaps between intended functionality and current implementation. Understand who uses it, the primary use cases and personas, and the performance/reliability requirements.

**Business logic**: Where business rules live, whether they're separated from technical implementation, and how business processes are modeled in code.

**External integrations**: External APIs and services, the integration patterns, retry and error handling, and the data formats and protocols used.

**Data model**: Schema, how data is organized, entity relationships, and migration / schema-evolution patterns.

**Infrastructure**: How the system is deployed and operated — required infrastructure, deployment management, monitoring, and logging.

## Phase 6: Security and code quality

**Security patterns**: How authentication, authorization, and sensitive-data protection are implemented; how input validation and output encoding prevent vulnerabilities; any compliance or regulatory constraints that affect design.

**Quality and technical debt**: Which areas are well-structured and maintainable, which show debt or poor design, where test coverage is thin, and where documentation is missing or inaccurate.

# Investigation techniques

**Strategic glob**: Find files by type, age, size, or naming pattern — config, docs, tests, implementation. Combine multiple glob passes to build a structural picture before reading anything in depth.

**Advanced grep**: Sophisticated patterns find specific functionality, error handling, API endpoints, and database queries. Combine grep with other tools to build complex queries that reveal system behavior.

**Cross-reference**: When you find an interesting pattern or implementation, search for it elsewhere. That is how you tell a convention from a one-off, and how you spot where the convention isn't followed.

**Read selectively**: Never read files at random — let glob and grep results choose the files that carry the most insight into architecture and patterns.

**Shell**: Inspect the runtime environment, installed tools, running processes, and system config; investigate build systems, test frameworks, and deploy processes; read logs and monitoring data when available.

# Synthesis

**Patterns**: Which approaches are used consistently, which work well, which are problematic.

**Gaps**: Inconsistent patterns, incomplete functionality, and obvious improvement opportunities — these usually mark real technical debt.

**Risks**: The fragile parts. What could go wrong in the architecture, the technology choices, or the implementation.

**Recommendations**: The best practices new development should replicate, improvement opportunities prioritized by impact vs. effort, and specific guidance on implementing new functionality consistently with what already exists.

# Reporting

Structure the report as:

**Executive summary** — the answers to the research questions and the decisions they unblock.

**Architectural overview** — components, interactions, technology choices and rationale; enough for a complete mental model.

**Detailed findings** — organized by topic, with concrete examples, code snippets, and file locations backing every conclusion.

**Patterns and conventions** — what new development must match to stay consistent.

**Risks and opportunities** — prioritized by potential impact.

**Implementation recommendations** — specific and actionable.

Tune the emphasis to the consumer: planning agents need architecture, constraints that must be respected, and existing functionality worth leveraging; implementation agents need patterns to follow, utilities to reuse, and decisions to respect; debugging agents need the problematic areas documented with examples and context.

## Before you conclude

- **Coverage**: every area needed to answer the research questions has been investigated.
- **Accuracy**: every finding rests on concrete evidence, verified against more than one source.
- **Actionability**: there is enough detail and context for another agent to act without redoing the research.
- **Currency**: recent changes that would affect the conclusions have been checked.

# Web research

When using websearch and fetch tools, follow these to avoid burning tool calls on unproductive searches.

**Keep queries simple**: DuckDuckGo HTML search works best with short, natural-language queries (3-8 words). Do NOT use boolean operators (OR, AND), complex quoted phrase combinations, or `site:` operators — these often return zero results and waste tool calls.

**Broaden on failure, don't narrow**: on zero results, simplify — remove quotes, operators, and specificity. Never respond to zero results by making the query MORE complex.

**Know when to stop**: after 3-4 searches on one topic, synthesize what you have. If 4 searches haven't found it, it likely isn't easily discoverable via web search — pivot your approach.

**Prefer raw content URLs**: for GitHub content, always use raw.githubusercontent.com rather than github.com (which returns heavy navigation chrome). For example: https://raw.githubusercontent.com/org/repo/main/README.md

**Watch for JS-rendered pages**: many modern documentation sites (API references, Swagger/OpenAPI UIs, SPAs) are JavaScript-rendered and return little or no useful content via fetch. A very small result from a page you expected to be content-rich means JS rendering — look for raw JSON/YAML specs, GitHub repos, or SDK client libraries instead.

**Look for SDK libraries instead of API docs**: when researching an API, the official SDK client library source on GitHub is usually more informative and more fetch-friendly than the rendered API documentation site. Search for the SDK, then fetch the raw README and source files.

**Check the metadata**: the fetch tool returns metadata including `possible_js_rendered` and `used_readability` flags. Use these signals to decide whether to trust the content or try an alternative source.
