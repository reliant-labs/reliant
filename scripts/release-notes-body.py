#!/usr/bin/env python3
"""Render docs/data/releases/<tag>.yaml into a GitHub Release body.

The hand-written YAML is the same source the Mintlify changelog page is built
from (`task generate:docs:changelog`), so the Release, the docs site and the
in-app changelog all say the same thing rather than drifting into three
descriptions of one release.

Prints nothing and exits 0 when the file is absent. That is deliberate: a tag
cut without notes should still get a Release carrying GitHub's auto-generated
commit list, which is strictly better than no Release at all.

With --generated, it prints the WHOLE Release body: the prose above, then the
commit list GitHub's generate-notes API returned (release.yml fetches it with
the previous final tag pinned), capped at --max-bytes. GitHub refuses a body
over 125000 characters with HTTP 422, and v1.33.0 and v1.34.0 hit it: their
Releases were never created, which skipped the CLI publish behind them. The
cap is in bytes, which are never fewer than characters, so a body that fits
in bytes fits GitHub's limit too. A body over the cap is cut at a line
boundary and ends with a link to the full comparison (--compare-url).

Usage: release-notes-body.py <tag> [--generated FILE [--compare-url URL] [--max-bytes N]]
"""

import argparse
import json
import os
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

# GitHub's limit is 125000 characters; stay clear of it.
MAX_BODY_BYTES = 120000
# The hand-written prose is a summary. Capped on its own so a runaway YAML can
# never crowd out the commit list, nor the commit list push the body over.
MAX_PROSE_BYTES = 30000


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


def render(path):
    """Render one release YAML to markdown ("" when it has no content)."""
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

    text = "\n".join(out).rstrip()
    return text + "\n" if text else ""


def cap(text, max_bytes, footer):
    """Return text whole when it fits in max_bytes, else cut at a line boundary.

    The cut keeps only whole lines, so no markdown line (or UTF-8 sequence) is
    split, and appends footer; the result, footer included, is at most
    max_bytes.
    """
    data = text.encode("utf-8")
    if len(data) <= max_bytes:
        return text
    tail = ("\n" + footer.rstrip("\n") + "\n").encode("utf-8")
    budget = max(0, max_bytes - len(tail))
    head = data[:budget]
    cut = head.rfind(b"\n")
    head = head[: cut + 1] if cut >= 0 else b""
    return (head + tail).decode("utf-8")


def main(argv):
    parser = argparse.ArgumentParser(prog="release-notes-body.py")
    parser.add_argument("tag")
    parser.add_argument("--generated", help="file holding GitHub's generated release notes")
    parser.add_argument("--compare-url", default="", help="the full comparison, linked when the body is cut")
    parser.add_argument("--max-bytes", type=int, default=MAX_BODY_BYTES)
    args = parser.parse_args(argv)

    tag = args.tag
    repo = os.environ.get("GITHUB_REPOSITORY", "reliant-labs/reliant")
    path = REPO_ROOT / "docs" / "data" / "releases" / f"{tag}.yaml"

    prose = ""
    if path.is_file():
        prose = cap(
            render(path),
            MAX_PROSE_BYTES,
            f"_The rest of these notes is in [docs/data/releases/{tag}.yaml]"
            f"(https://github.com/{repo}/blob/{tag}/docs/data/releases/{tag}.yaml)._",
        )
    # Absent notes are not an error — see the module docstring.

    if args.generated is None:
        sys.stdout.write(prose)
        return 0

    generated = Path(args.generated).read_text(encoding="utf-8").strip()
    body = "\n".join(part.rstrip("\n") + "\n" for part in (prose, generated) if part.strip())
    if args.compare_url:
        footer = f"_Truncated to fit GitHub's limit. Every change: {args.compare_url}_"
    else:
        footer = "_Truncated to fit GitHub's limit._"
    sys.stdout.write(cap(body, args.max_bytes, footer))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
