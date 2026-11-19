package templates

import (
	"regexp"
	"strings"
	"testing"
)

// TestCanonicalTSImportOrder pins the ordering contract the scaffolded
// eslint config enforces. The cases are the shapes the page templates
// actually emit — a conditional external (lucide-react appears only for an
// entity with a Create RPC), a render-time internal path (the service's
// hooks module, whose name is the SERVICE's, not a constant), and a
// type-only import — plus the two bail-outs where rearranging would be
// wrong rather than merely unnecessary.
func TestCanonicalTSImportOrder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "external group alphabetised, banner stays above the block",
			in: `"use client";

// yours: scaffolded once, never touched again
// Detail page emitted by ` + "`forge scaffold page`" + `.
import { Trash2 } from "lucide-react";
import { userMessage } from "@reliantlabs/forge-web-runtime";
import Link from "next/link";

export default function P() {}
`,
			want: `"use client";

// yours: scaffolded once, never touched again
// Detail page emitted by ` + "`forge scaffold page`" + `.
import { userMessage } from "@reliantlabs/forge-web-runtime";
import { Trash2 } from "lucide-react";
import Link from "next/link";

export default function P() {}
`,
		},
		{
			name: "render-time internal paths sort among the fixed ones",
			in: `import { Resource } from "@reliantlabs/forge-web-runtime";

import { useQueryResource } from "@/hooks/use-query-resource";
import { formatValue } from "@/lib/format-utils";
import { useListProducts } from "@/hooks/catalog-service-hooks";
import { ProductStatus } from "@/gen/services/catalog/v1/catalog_pb";

import type { Product } from "@/gen/services/catalog/v1/catalog_pb";

const columns = [];
`,
			want: `import { Resource } from "@reliantlabs/forge-web-runtime";

import { ProductStatus } from "@/gen/services/catalog/v1/catalog_pb";
import { useListProducts } from "@/hooks/catalog-service-hooks";
import { useQueryResource } from "@/hooks/use-query-resource";
import { formatValue } from "@/lib/format-utils";

import type { Product } from "@/gen/services/catalog/v1/catalog_pb";

const columns = [];
`,
		},
		{
			name: "a service named after the alphabet's end sorts the other way",
			in: `import { useQueryResource } from "@/hooks/use-query-resource";
import { useListWidgets } from "@/hooks/warehouse-service-hooks";
`,
			want: `import { useQueryResource } from "@/hooks/use-query-resource";
import { useListWidgets } from "@/hooks/warehouse-service-hooks";
`,
		},
		{
			name: "groups: builtin, external, internal, parent, sibling, index",
			in: `import { useState } from "react";
import x from "./x";
import fs from "node:fs";
import idx from ".";
import { up } from "../up";
import { a } from "@/a";
`,
			want: `import fs from "node:fs";

import { useState } from "react";

import { a } from "@/a";

import { up } from "../up";

import x from "./x";

import idx from ".";
`,
		},
		{
			name: "segment-wise comparison: next/link precedes next-auth",
			in: `import { auth } from "next-auth";
import Link from "next/link";
`,
			want: `import Link from "next/link";
import { auth } from "next-auth";
`,
		},
		{
			name: "a comment between imports travels with the import below it",
			in: `import { z } from "zod";
// useRouter is only consumed by the delete flow's redirect.
import { useRouter } from "next/navigation";
`,
			want: `// useRouter is only consumed by the delete flow's redirect.
import { useRouter } from "next/navigation";
import { z } from "zod";
`,
		},
		{
			name: "a side-effect import makes evaluation order semantic: leave the block alone",
			in: `import "./polyfills";
import { z } from "zod";
import { a } from "@/a";
`,
			want: `import "./polyfills";
import { z } from "zod";
import { a } from "@/a";
`,
		},
		{
			name: "no leading import block: untouched",
			in: `const later = await import("zod");
import { a } from "@/a";
`,
			want: `const later = await import("zod");
import { a } from "@/a";
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(CanonicalTSImportOrder([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("CanonicalTSImportOrder mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
			// A formatter that is not a fixed point rewrites files on every
			// run, which for a scaffold-once page means byte drift nobody
			// asked for.
			again := string(CanonicalTSImportOrder([]byte(got)))
			if again != got {
				t.Errorf("not idempotent\n--- first ---\n%s\n--- second ---\n%s", got, again)
			}
		})
	}
}

// TestTSImportOrderMatchesESLintConfig is the anti-drift gate between the two
// halves of this contract: the Go emitter that ORDERS the imports and the
// eslint config that CHECKS them. They are necessarily separate artifacts —
// one is Go, the other is the JavaScript config shipped into every scaffold —
// so the only thing keeping them from becoming two independent sources of
// truth is this test failing when they disagree.
//
// Only the nextjs config configures import/order today; the Vite SPA config
// does not, and its pages are normalized to the same order anyway so that
// turning the rule on there is a no-op rather than a migration.
func TestTSImportOrderMatchesESLintConfig(t *testing.T) {
	t.Parallel()

	raw, err := FrontendTemplates().Get("nextjs/eslint.config.mjs")
	if err != nil {
		t.Fatalf("read nextjs/eslint.config.mjs: %v", err)
	}
	config := string(raw)

	// Read the options out of the config text from the rule name onward.
	// Anchoring on indentation would make this test fail on a reformat; the
	// options below appear nowhere else in the file, so "first match after
	// the rule name" is both simpler and stabler.
	at := strings.Index(config, `"import/order":`)
	if at < 0 {
		t.Fatalf("no import/order rule in nextjs/eslint.config.mjs — CanonicalTSImportOrder orders " +
			"emitted imports to satisfy a rule the scaffold no longer configures")
	}
	body := config[at:]

	groupsBlock := regexp.MustCompile(`(?s)groups:\s*\[(.*?)\]`).FindStringSubmatch(body)
	if groupsBlock == nil {
		t.Fatalf("import/order carries no explicit `groups` array; the emitter mirrors one:\n%s", body)
	}
	var groups []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(groupsBlock[1], -1) {
		groups = append(groups, m[1])
	}
	if strings.Join(groups, ",") != strings.Join(tsImportGroups, ",") {
		t.Errorf("eslint groups %v != tsImportGroups %v — emitted pages would be sorted into a "+
			"different order than the scaffold's own `npm run lint` demands", groups, tsImportGroups)
	}

	if m := regexp.MustCompile(`"newlines-between":\s*"([^"]+)"`).FindStringSubmatch(body); m == nil || m[1] != "always" {
		t.Errorf(`newlines-between is %v, emitter assumes "always" (one blank line between groups, none within)`, m)
	}
	if !regexp.MustCompile(`alphabetize:\s*\{[^}]*order:\s*"asc"`).MatchString(body) {
		t.Errorf("alphabetize.order is not \"asc\"; the emitter sorts ascending:\n%s", body)
	}
	if !regexp.MustCompile(`alphabetize:\s*\{[^}]*caseInsensitive:\s*true`).MatchString(body) {
		t.Errorf("alphabetize.caseInsensitive is not true; the emitter lowercases before comparing:\n%s", body)
	}

	m := regexp.MustCompile(`"import/internal-regex":\s*"([^"]+)"`).FindStringSubmatch(config)
	if m == nil {
		t.Fatalf("no import/internal-regex setting — without it eslint cannot classify @/ aliases " +
			"deterministically and the emitter's `internal` group is a guess")
	}
	if m[1] != tsInternalRegex.String() {
		t.Errorf("import/internal-regex is %q, emitter uses %q", m[1], tsInternalRegex.String())
	}
}
