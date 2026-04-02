#!/usr/bin/env python3
"""Generate Mintlify-safe reference MDX pages from generated docs-source markdown.

This keeps Mintlify reference pages reproducible without hand-maintaining copies of
generated docs.
"""

from __future__ import annotations

import posixpath
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = ROOT / "generated" / "docs-source" / "reference"
TARGET_DIR = ROOT / "docs" / "reference"

PAGE_CONFIG = {
    "types": {
        "title": "Types Reference",
        "description": "Reference documentation for workflow config types and output helper types",
    },
    "scenario-schema": {
        "title": "Scenario Schema",
        "description": "Reference documentation for workflow testing scenarios",
    },
    "nodes": {
        "title": "Node Types Reference",
        "description": "Auto-generated API reference for workflow node type Input/Output schemas",
    },
    "tools": {
        "title": "Tools Quick Reference",
        "description": "Auto-generated quick reference for all tools (tags, names, descriptions)",
    },
    "models": {
        "title": "Models Reference",
        "description": "Available AI models and their capabilities",
    },
    "workflow-schema": {
        "title": "Workflow Schema Reference",
        "description": "Top-level workflow, edge, and edge-case schema reference",
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


def wrap_inline_mdx_sensitive_tokens(body: str) -> str:
    bracketed_object_array_re = re.compile(r"\[\{[^{}\n]+\}\]")
    brace_token_re = re.compile(
        r"(?:[A-Za-z0-9_./*?\\-]+)?\{[^{}\n]*\}(?:[A-Za-z0-9_./*?\\-]+)?"
    )
    angle_placeholder_re = re.compile(r"<[A-Za-z0-9_.:-]+(?:-[A-Za-z0-9_.:-]+)*>")

    def wrap_segment(segment: str) -> str:
        protected: list[str] = []

        def protect(match: re.Match[str]) -> str:
            protected.append(match.group(0))
            return f"__BRACKETED_OBJECT_ARRAY_{len(protected) - 1}__"

        segment = bracketed_object_array_re.sub(protect, segment)
        segment = brace_token_re.sub(lambda m: f"`{m.group(0)}`", segment)
        segment = angle_placeholder_re.sub(lambda m: f"`{m.group(0)}`", segment)

        for idx, original in enumerate(protected):
            segment = segment.replace(
                f"__BRACKETED_OBJECT_ARRAY_{idx}__", f"`{original}`"
            )

        return segment

    lines = body.splitlines()
    out: list[str] = []
    in_fence = False

    for line in lines:
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue

        if in_fence or (("{" not in line or "}" not in line) and ("<" not in line or ">" not in line)):
            out.append(line)
            continue

        parts = line.split("`")
        for idx in range(0, len(parts), 2):
            if (("{" in parts[idx] and "}" in parts[idx]) or ("<" in parts[idx] and ">" in parts[idx])):
                parts[idx] = wrap_segment(parts[idx])
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

    # Normalize excess blank lines introduced by comment/frontmatter stripping.
    body = re.sub(r"\n{3,}", "\n\n", body).strip() + "\n"
    return body


def generate_page(slug: str) -> None:
    source_path = SOURCE_DIR / f"{slug}.md"
    target_path = TARGET_DIR / f"{slug}.mdx"

    if not source_path.exists():
        raise FileNotFoundError(f"Missing generated source: {source_path}")

    raw = source_path.read_text()
    metadata, body = parse_frontmatter(raw)
    title = metadata.get("title") or PAGE_CONFIG[slug]["title"]
    description = metadata.get("description") or PAGE_CONFIG[slug]["description"]
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
        f"Regenerate with: make generate-mintlify-reference\n"
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
