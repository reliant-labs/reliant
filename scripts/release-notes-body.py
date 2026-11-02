#!/usr/bin/env python3
"""Render docs/data/releases/<tag>.yaml into a GitHub Release body.

The hand-written YAML is the same source the Mintlify changelog page is built
from (`task generate:docs:changelog`), so the Release, the docs site and the
in-app changelog all say the same thing rather than drifting into three
descriptions of one release.

Prints nothing and exits 0 when the file is absent. That is deliberate: a tag
cut without notes should still get a Release carrying GitHub's auto-generated
commit list, which is strictly better than no Release at all. The caller
appends that list via `gh release create --generate-notes`.

Usage: release-notes-body.py <tag>
"""

import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def load(path):
    """Parse the subset of YAML these files use.

    Deliberately not PyYAML: the release workflow runs on a bare runner and
    adding a pip install to the release path buys nothing here. The schema is
    fixed and enforced by `task generate:docs:changelog`, which parses the same
    files — four scalars and either a list of title/description pairs
    (`items`) or titled groups of them (`sections`, what
    `task generate:docs:changelog:draft` writes).

    Returns (meta, entries); an entry is a dict with a title, an optional
    description and `section`: True for a group heading, False for an entry
    inside a group, None for a top-level item.
    """
    text = path.read_text(encoding="utf-8")
    meta = {}
    entries = []
    current = None
    in_sections = False
    section_indent = None

    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue

        stripped = raw.strip()
        indent = len(raw) - len(raw.lstrip())

        if stripped.startswith("- title:"):
            if current:
                entries.append(current)
            kind = None
            if in_sections:
                if section_indent is None:
                    section_indent = indent
                kind = indent == section_indent
            current = {"title": unquote(stripped[len("- title:"):]), "section": kind}
            continue

        if current is not None and stripped.startswith("description:"):
            current["description"] = unquote(stripped[len("description:"):])
            continue

        if indent == 0 and ":" in stripped:
            key, _, value = stripped.partition(":")
            in_sections = key.strip() == "sections"
            value = unquote(value)
            if value:
                meta[key.strip()] = value

    if current:
        entries.append(current)
    return meta, entries


def unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] == '"':
        # A JSON string is a YAML double-quoted scalar (the draft writes them so).
        try:
            return json.loads(value)
        except ValueError:
            return value[1:-1]
    if len(value) >= 2 and value[0] == value[-1] == "'":
        return value[1:-1].replace("''", "'")
    return value


def main():
    if len(sys.argv) != 2:
        print("usage: release-notes-body.py <tag>", file=sys.stderr)
        return 2

    tag = sys.argv[1]
    path = REPO_ROOT / "docs" / "data" / "releases" / f"{tag}.yaml"
    if not path.is_file():
        # Not an error — see the module docstring.
        return 0

    meta, entries = load(path)

    out = []
    if meta.get("title"):
        out.append(f"## {meta['title']}")
        out.append("")
    if meta.get("summary"):
        out.append(meta["summary"])
        out.append("")
    for entry in entries:
        if not entry.get("title"):
            continue
        if entry["section"] is True:
            if out and out[-1] != "":
                out.append("")
            out.append(f"### {entry['title']}")
            out.append("")
        elif entry["section"] is False:
            line = f"- **{entry['title']}**"
            if entry.get("description"):
                line += f" {entry['description']}"
            out.append(line)
        else:
            out.append(f"### {entry['title']}")
            if entry.get("description"):
                out.append("")
                out.append(entry["description"])
            out.append("")

    sys.stdout.write("\n".join(out).rstrip() + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
