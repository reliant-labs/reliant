#!/usr/bin/env python3
"""Generate Mintlify-safe reference MDX pages from generated docs-source markdown.

This keeps Mintlify reference pages reproducible without hand-maintaining copies of
generated docs.
"""

from __future__ import annotations

import posixpath
import re
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = ROOT / "generated" / "docs-source" / "reference"
TARGET_DIR = ROOT / "docs" / "reference"

# The title and description of each page. These win over any frontmatter the
# source generator wrote: they are the page's search-result title and snippet,
# so they are written for a reader searching, and kept here in one place.
PAGE_CONFIG = {
    "types": {
        "title": "Types Reference",
        "description": "Workflow config types (inject, project, thread, sub-workflow, save-message) and the output types nodes return: messages, thinking, tool calls and tool results.",
    },
    "scenario-schema": {
        "title": "Scenario Schema",
        "description": "Schema for workflow test scenarios: simulated events, mocked tool calls, expectations, node targeting and a complete example for reliant scenario run.",
    },
    "nodes": {
        "title": "Node Types Reference",
        "description": "Every workflow node type, generated from the proto: what each node does and the input fields and output fields it reads and writes.",
    },
    "tools": {
        "title": "Tools Quick Reference",
        "description": "Every tool a Reliant agent can call, by tag: what it does, its parameters and when to use it. Generated from the live tool registry.",
    },
    "models": {
        "title": "Models Reference",
        "description": "The Claude, GPT and Gemini models Reliant supports: context windows, capabilities, model tags, and how to choose and configure a model per workflow.",
    },
    "cli": {
        "title": "CLI Reference",
        "description": "Every reliant CLI command, subcommand and flag, generated from the command tree: daemons, workflows, triggers, machines, projects, worktrees, auth and forge.",
    },
    "workflow-schema": {
        "title": "Workflow Schema Reference",
        "description": "Top-level workflow YAML schema: workflow fields, edges and edge cases, concurrency limits, checks, and the YAML-only syntax sugar.",
    },
}

ICON_MAP = {
    "wrench-screwdriver": "🛠️",
    "paper-clip": "📎",
    "save": "💾",
    "lightning-bolt": "⚡",
    "light-bulb": "💡",
}

RELREF_RE = re.compile(r"\{\{<\s*relref\s+\"([^\"]+)\"\s*>\}\}")
ICON_RE = re.compile(r"\{\{<\s*icon\s+\"([^\"]+)\"\s*>\}\}")
HTML_COMMENT_RE = re.compile(r"<!--\s*[\s\S]*?\s*-->\n*", re.MULTILINE)
FRONTMATTER_RE = re.compile(r"\A---\n([\s\S]*?)\n---\n?", re.MULTILINE)
LEADING_H1_RE = re.compile(r"\A#\s+(.+?)\n(?:\n+)?", re.MULTILINE)


def parse_frontmatter(text: str) -> tuple[dict[str, str], str]:
    match = FRONTMATTER_RE.match(text)
    if not match:
        return {}, text

    metadata: dict[str, str] = {}
    for raw_line in match.group(1).splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or ":" not in line:
            continue
        key, value = line.split(":", 1)
        key = key.strip()
        value = value.strip().strip('"')
        metadata[key] = value

    return metadata, text[match.end():]


def resolve_relref(ref: str, current_section: str) -> str:
    if ref.startswith("/docs/"):
        return "/" + ref[len("/docs/"):].lstrip("/")
    if ref.startswith("/"):
        return ref

    resolved = posixpath.normpath(posixpath.join("/" + current_section, ref))
    if resolved.startswith("/docs/"):
        resolved = "/" + resolved[len("/docs/"):].lstrip("/")
    return resolved


def yaml_quote(value: str) -> str:
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"') + '"'


# Tool descriptions introduce raw YAML examples under these bold headings.
YAML_EXAMPLE_HEADINGS = ("**Scenario YAML structure:**", "**Events — typed mode")


def fence_example_blocks(body: str) -> str:
    lines = body.splitlines()
    out: list[str] = []
    in_fence = False
    i = 0

    def previous_nonempty(idx: int) -> str:
        j = idx - 1
        while j >= 0:
            candidate = lines[j].strip()
            if candidate:
                return candidate
            j -= 1
        return ""

    def should_start_example_block(line: str, prev: str) -> bool:
        stripped = line.strip()
        if stripped in {"{", "{}", "tasks: [", "edits: ["}:
            return True
        if stripped.startswith("{") and stripped.endswith("}"):
            return True
        if stripped.endswith(": [") and "{" in stripped:
            return True
        if stripped == "[" and "Example" in prev:
            return True
        return False

    while i < len(lines):
        line = lines[i]
        stripped = line.strip()

        if stripped.startswith("```"):
            in_fence = not in_fence
            out.append(line)
            i += 1
            continue

        prev = previous_nonempty(i)
        if (
            not in_fence
            and stripped
            and i > 0
            and lines[i - 1].strip().startswith(YAML_EXAMPLE_HEADINGS)
        ):
            block = []
            while i < len(lines) and lines[i].strip() != "":
                block.append(lines[i])
                i += 1
            out.append("```yaml")
            out.extend(block)
            out.append("```")
            continue

        if not in_fence and should_start_example_block(line, prev):
            block: list[str] = []
            while i < len(lines) and lines[i].strip() != "":
                block.append(lines[i].lstrip())
                i += 1
            out.append("```json")
            out.extend(block)
            out.append("```")
            if i < len(lines) and lines[i].strip() == "":
                out.append(lines[i])
                i += 1
            continue

        out.append(line)
        i += 1

    return "\n".join(out)


def escape_all_angles(body: str) -> str:
    """Escape every `<` outside code. For pages generated from plain-text sources
    (CLI help) that contain no JSX, where prose like `<step name>` must not parse as a tag."""
    out: list[str] = []
    in_fence = False
    for line in body.splitlines():
        if line.strip().startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue
        if in_fence:
            out.append(line)
            continue
        parts = line.split("`")
        for idx in range(0, len(parts), 2):
            parts[idx] = re.sub(r"(?<!\\)<", r"\\<", parts[idx])
        out.append("`".join(parts))
    return "\n".join(out)


def wrap_inline_mdx_sensitive_tokens(body: str) -> str:
    bracketed_object_array_re = re.compile(r"\[\{[^{}\n]+\}\]")
    brace_token_re = re.compile(
        r"(?:[A-Za-z0-9_./*?\\-]+)?\{[^{}\n]*\}(?:[A-Za-z0-9_./*?\\-]+)?"
    )
    angle_placeholder_re = re.compile(r"<[@#!]?[A-Za-z0-9_.:-]+(?:-[A-Za-z0-9_.:-]+)*>")

    def wrap_segment(segment: str) -> str:
        protected: list[str] = []

        def protect(match: re.Match[str]) -> str:
            protected.append(match.group(0))
            return f"__PROTECTED_CODE_SPAN_{len(protected) - 1}__"

        segment = bracketed_object_array_re.sub(protect, segment)
        # A brace token is protected too, so the placeholder pass below cannot
        # wrap a `<name>` inside it a second time: nested backticks would close
        # the code span early and leave `<name>` to parse as an unclosed tag.
        segment = brace_token_re.sub(protect, segment)
        segment = angle_placeholder_re.sub(lambda m: f"`{m.group(0)}`", segment)

        placeholder_re = re.compile(r"__PROTECTED_CODE_SPAN_(\d+)__")

        def raw(text: str) -> str:
            # A span protected inside another (an object array inside a brace
            # token) is already in the outer span's code: restore it unwrapped.
            return placeholder_re.sub(lambda m: raw(protected[int(m.group(1))]), text)

        return placeholder_re.sub(
            lambda m: f"`{raw(protected[int(m.group(1))])}`", segment
        )

    lines = body.splitlines()
    out: list[str] = []
    in_fence = False

    for line in lines:
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue

        if in_fence or (("{" not in line or "}" not in line) and "<" not in line and "{" not in line and "}" not in line):
            out.append(line)
            continue

        parts = line.split("`")
        for idx in range(0, len(parts), 2):
            if (("{" in parts[idx] and "}" in parts[idx]) or ("<" in parts[idx] and ">" in parts[idx])):
                parts[idx] = wrap_segment(parts[idx])
        # Braces left outside code (e.g. spanning inline code) would parse as MDX expressions.
        resplit = "`".join(parts).split("`")
        for idx in range(0, len(resplit), 2):
            resplit[idx] = re.sub(r"(?<!\\)([{}])", r"\\\1", resplit[idx])
            # A truncated or non-tag `<` (e.g. "<@ID...") would open a JSX tag.
            resplit[idx] = re.sub(r"(?<!\\)<(?=[@#!])", r"\\<", resplit[idx])
            # A `<` that cannot start a tag (heredocs like <<<"$X", "a < b").
            resplit[idx] = re.sub(r"(?<!\\)<(?![A-Za-z/\\\\])", r"\\<", resplit[idx])
        out.append("`".join(resplit))

    return "\n".join(out)


# An opening quote is taken along, so a quoted URL that the autolink runs into
# the closing quote of is wrapped whole: `"http://x:1"`, not "`http://x:1"`.
BARE_URL_RE = re.compile(r"(?P<quote>[\"']?)(?P<url>https?://[^\s<>`|)]+)")


def is_valid_url(url: str) -> bool:
    try:
        parts = urlsplit(url)
        parts.port  # raises on a port that is not a number
    except ValueError:
        return False
    return bool(parts.hostname)


def code_invalid_bare_urls(body: str) -> str:
    """Wrap a bare URL that is not a valid URL in backticks.

    MDX turns a bare `https://…` in prose into a link (GFM autolink literal),
    and Mintlify refuses the whole page when that link is not a valid URL:
    CLI help placeholders such as `https://host[:port]`, or a URL that runs
    into a closing quote. In code it is text, which is what such a URL is.
    """
    out: list[str] = []
    in_fence = False
    for line in body.splitlines():
        if line.strip().startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue
        if in_fence:
            out.append(line)
            continue
        parts = line.split("`")
        for idx in range(0, len(parts), 2):
            parts[idx] = BARE_URL_RE.sub(
                lambda m: m.group(0) if is_valid_url(m.group("url")) else f"`{m.group(0)}`",
                parts[idx],
            )
        out.append("`".join(parts))
    return "\n".join(out)


def convert_body(body: str, slug: str) -> str:
    body = body.replace("\r\n", "\n")
    body = HTML_COMMENT_RE.sub("", body)
    body = body.lstrip()

    title = PAGE_CONFIG[slug]["title"]
    h1_match = LEADING_H1_RE.match(body)
    if h1_match and h1_match.group(1).strip() == title:
        body = body[h1_match.end():]

    body = RELREF_RE.sub(lambda m: resolve_relref(m.group(1), "reference"), body)
    body = body.replace("(/docs/", "(/")
    body = ICON_RE.sub(lambda m: ICON_MAP.get(m.group(1), m.group(1)), body)
    body = fence_example_blocks(body)
    body = wrap_inline_mdx_sensitive_tokens(body)
    body = code_invalid_bare_urls(body)
    if slug == "cli":
        body = escape_all_angles(body)

    # Normalize excess blank lines introduced by comment/frontmatter stripping.
    body = re.sub(r"\n{3,}", "\n\n", body).strip() + "\n"
    check_parses_as_mdx(body, slug)
    return body


def check_parses_as_mdx(body: str, slug: str) -> None:
    """Fail the generator, not Mintlify, on what makes Mintlify refuse a page.

    The reference pages are generated from plain text and hold no JSX, so a tag
    outside a code span is always a conversion bug: MDX parses it as an
    unclosed element. A bare URL that is not a valid URL fails Mintlify's link
    parsing. Either way the whole page (and `mint broken-links`) fails.
    """
    in_fence = False
    for lineno, line in enumerate(body.splitlines(), 1):
        if line.strip().startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        parts = line.split("`")
        for idx in range(0, len(parts), 2):
            for url in (m.group("url") for m in BARE_URL_RE.finditer(parts[idx])):
                if not is_valid_url(url):
                    raise ValueError(
                        f"{slug}.mdx body line {lineno}: bare URL {url!r} is not a valid URL "
                        f"and would fail Mintlify's link parsing: {line.strip()[:160]}"
                    )
            match = re.search(r"(?<!\\)<[A-Za-z/][^>]*>", parts[idx])
            if match:
                raise ValueError(
                    f"{slug}.mdx body line {lineno}: {match.group(0)!r} is outside code "
                    f"and would parse as an unclosed MDX tag: {line.strip()[:160]}"
                )


def generate_page(slug: str) -> None:
    source_path = SOURCE_DIR / f"{slug}.md"
    target_path = TARGET_DIR / f"{slug}.mdx"

    if not source_path.exists():
        raise FileNotFoundError(f"Missing generated source: {source_path}")

    raw = source_path.read_text()
    metadata, body = parse_frontmatter(raw)
    title = PAGE_CONFIG[slug].get("title") or metadata.get("title")
    description = PAGE_CONFIG[slug].get("description") or metadata.get("description")
    body = convert_body(body, slug)

    output = (
        f"---\n"
        f"title: {yaml_quote(title)}\n"
        f"description: {yaml_quote(description)}\n"
        f"---\n\n"
        f"{{/*\n"
        f"GENERATED FILE - DO NOT EDIT DIRECTLY\n\n"
        f"Source: generated/docs-source/reference/{slug}.md\n"
        f"Generated by: scripts/generate-mintlify-reference.py\n"
        f"Regenerate with: task generate:docs:mintlify-reference\n"
        f"*/}}\n\n"
        f"{body}"
    )

    target_path.parent.mkdir(parents=True, exist_ok=True)
    target_path.write_text(output)
    print(f"Generated {target_path.relative_to(ROOT)}")


def main() -> None:
    for slug in PAGE_CONFIG:
        generate_page(slug)


if __name__ == "__main__":
    main()
