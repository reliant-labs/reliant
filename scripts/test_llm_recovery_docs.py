#!/usr/bin/env python3
"""Scoped, dependency-free checks for the public LLM recovery entry points."""
import json
from pathlib import Path
import re
import unittest

DOCS = Path(__file__).resolve().parents[1] / "docs"
GUIDE = "forge/llm-provider"


class LLMRecoveryDocsTest(unittest.TestCase):
    def test_guide_is_in_navigation(self):
        nav = json.loads((DOCS / "docs.json").read_text())
        self.assertIn(f'"{GUIDE}"', json.dumps(nav["navigation"]))
        self.assertTrue((DOCS / f"{GUIDE}.mdx").is_file())

    def test_local_links_and_fragments(self):
        for route in (GUIDE, "forge/deploy-without-an-account"):
            text = (DOCS / f"{route}.mdx").read_text()
            for link in re.findall(r"\]\((/[^)]+)\)", text):
                page, _, fragment = link.partition("#")
                target = DOCS / f"{page.lstrip('/')}.mdx"
                self.assertTrue(target.is_file(), f"{route}: missing {link}")
                if fragment:
                    headings = re.findall(r"^#{1,6}\s+(.+)$", target.read_text(), re.M)
                    anchors = [re.sub(r"[^\w -]", "", h.lower()).replace(" ", "-") for h in headings]
                    self.assertIn(fragment, anchors, link)

    def test_recovery_distinguishes_proof_from_configuration(self):
        text = (DOCS / f"{GUIDE}.mdx").read_text()
        for required in ("no outbound internet", "Never put a provider key in browser code", "stay blank", "forge login", "forge secret set --env prod OPENAI_API_KEY", 'forge.ManagedSecret {name = "OPENAI_API_KEY"}', "forge env deploy prod", "actual server-side request", "not** proof of a live claim"):
            self.assertIn(required, text)
        entry = (DOCS / "forge/deploy-without-an-account.mdx").read_text()
        self.assertIn(f"](/{GUIDE})", entry)
        self.assertIn("## What claiming gives you", entry)


if __name__ == "__main__":
    unittest.main()
