#!/usr/bin/env python3
"""Render docs/data/releases/<tag>.yaml into a GitHub Release body.

The hand-written YAML is the same source the Mintlify changelog page is built
from (`make generate-changelog`), so the Release, the docs site and the
in-app changelog all say the same thing rather than drifting into three
descriptions of one release.

Prints nothing and exits 0 when the file is absent. That is deliberate: a tag
cut without notes should still get a Release carrying GitHub's auto-generated
commit list, which is strictly better than no Release at all. The caller
appends that list via `gh release create --generate-notes`.

Usage: release-notes-body.py <tag>
"""

import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def load(path):
    """Parse the subset of YAML these files use.

    Deliberately not PyYAML: the release workflow runs on a bare runner and
    adding a pip install to the release path buys nothing here. The schema is
    fixed and enforced by `make generate-changelog`, which parses the same
    files — it is four scalars and a list of title/description pairs.
    """
    text = path.read_text(encoding="utf-8")
    meta = {}
    items = []
    current = None

    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue

        stripped = raw.strip()

        if stripped.startswith("- title:"):
            if current:
                items.append(current)
            current = {"title": unquote(stripped[len("- title:"):])}
            continue

        if current is not None and stripped.startswith("description:"):
            current["description"] = unquote(stripped[len("description:"):])
            continue

        if not raw.startswith(" ") and ":" in stripped:
            key, _, value = stripped.partition(":")
            value = unquote(value)
            if value:
                meta[key.strip()] = value

    if current:
        items.append(current)
    return meta, items


def unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
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

    meta, items = load(path)

    out = []
    if meta.get("title"):
        out.append(f"## {meta['title']}")
        out.append("")
    if meta.get("summary"):
        out.append(meta["summary"])
        out.append("")
    for item in items:
        if not item.get("title"):
            continue
        out.append(f"### {item['title']}")
        if item.get("description"):
            out.append("")
            out.append(item["description"])
        out.append("")

    sys.stdout.write("\n".join(out).rstrip() + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
