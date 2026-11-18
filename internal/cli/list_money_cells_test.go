package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// moneyPageForTest builds the list-page data for an entity with the given
// fields, the way generate does: the CRUD shape from the service, then the
// entity's columns. Every field named *_cents is an int64 money column, and a
// `currency` string field is the sibling the money cells format against.
func moneyPageForTest(t *testing.T, entity string, fields []string) codegen.PageTemplateData {
	t.Helper()
	pkg := "services.billing.v1"
	fq := pkg + "." + entity
	var schema []codegen.SchemaFieldDef
	var entityFields []codegen.EntityField
	for _, name := range append([]string{"id"}, fields...) {
		kind := "string"
		if strings.HasSuffix(name, "_cents") {
			kind = "int64"
		}
		schema = append(schema, codegen.SchemaFieldDef{Name: name, Kind: kind})
		entityFields = append(entityFields, codegen.EntityField{Name: name, ProtoType: kind, Kind: codegen.FieldKindScalar})
	}
	plural := entity + "s"
	svc := codegen.ServiceDef{
		Name:      "BillingService",
		Package:   pkg,
		ProtoFile: "proto/services/billing/v1/billing.proto",
		Methods: []codegen.Method{
			{Name: "List" + plural, InputType: "List" + plural + "Request", OutputType: "List" + plural + "Response",
				InputTypeFQ: pkg + ".List" + plural + "Request"},
			{Name: "Get" + entity, InputType: "Get" + entity + "Request", OutputType: "Get" + entity + "Response"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"List" + plural + "Response": {
				{Name: strings.ToLower(plural), ProtoType: "[]message", MessageType: fq},
			},
		},
		Schemas:     map[string][]codegen.SchemaFieldDef{fq: schema},
		SchemaFiles: map[string]string{fq: "proto/services/billing/v1/billing.proto"},
	}
	pages := codegen.ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	codegen.AttachEntityMeta(&page, codegen.EntityDef{
		Name:      entity,
		PkField:   "id",
		ProtoFile: "proto/services/billing/v1/billing.proto",
		Fields:    entityFields,
	}, svc)
	return page
}

// TestPageTemplates_ListMoneyCellsKeepCentsAndCurrency pins how a money
// column reaches the list table, now that every cell renders through the
// shared listCellValue partial and the primary cell is wrapped in the
// record's <Link>. A regression in either path (the cents formatter
// dropped, the row's own currency lost, the value left outside its JSX
// expression) shows a reader the wrong amount, or no amount.
//
// Charge: a PRIMARY money column (amount_cents, the first column, so the
// link) and a non-primary one (tip_cents), both against the row's currency.
// Fee: a non-primary money column with no currency field, which keeps the
// formatter's USD default.
func TestPageTemplates_ListMoneyCellsKeepCentsAndCurrency(t *testing.T) {
	charge := moneyPageForTest(t, "Charge", []string{"amount_cents", "tip_cents", "currency"})
	fee := moneyPageForTest(t, "Fee", []string{"label", "fee_cents"})
	if got := charge.PrimaryListColumn(); got != "amountCents" {
		t.Fatalf("Charge's primary list column = %q, want amountCents — the fixture no longer covers a linked money cell", got)
	}

	cases := []struct {
		name string
		page codegen.PageTemplateData
		want []string
	}{
		{"Charge", charge, []string{
			// Primary: the formatted amount, as a JSX expression, inside the link.
			"{formatMoneyCents(item.amountCents, item.currency)}",
			"cell: (item) => formatMoneyCents(item.tipCents, item.currency),",
		}},
		{"Fee", fee, []string{
			"{formatValue(item.label)}",
			"cell: (item) => formatMoneyCents(item.feeCents),",
		}},
	}
	for _, tc := range cases {
		for _, dir := range []string{"pages", "vite-spa-pages"} {
			list := renderPageInDirForTest(t, dir, "list-page.tsx.tmpl", tc.page)
			for _, want := range append([]string{
				`import { formatMoneyCents, formatValue } from "@/lib/format-utils";`,
			}, tc.want...) {
				if !strings.Contains(list, want) {
					t.Errorf("%s/list-page (%s) missing %q:\n%s", dir, tc.name, want, list)
				}
			}
			// The primary money cell is the link's child, not a sibling of it.
			if tc.name == "Charge" {
				link := regexp.MustCompile(`(?s)<Link\b[^>]*>\s*\{formatMoneyCents\(item\.amountCents, item\.currency\)\}\s*</Link>`)
				if !link.MatchString(list) {
					t.Errorf("%s/list-page: the primary money cell is not the record link's content:\n%s", dir, list)
				}
			}
			if strings.Contains(list, "formatValue(item.amountCents)") || strings.Contains(list, "formatValue(item.feeCents)") {
				t.Errorf("%s/list-page (%s) prints a money column through formatValue — the raw cents, not the amount:\n%s", dir, tc.name, list)
			}
		}
	}

	t.Run("amounts", func(t *testing.T) {
		if testing.Short() {
			t.Skip("spawns node to run the shipped format-utils.ts; runs in full mode")
		}
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node not on PATH")
		}
		// Every money cell the Next.js list pages render, run against the
		// SHIPPED formatter: the template's expression, not a re-typed one.
		call := regexp.MustCompile(`formatMoneyCents\(item\.\w+(?:, item\.\w+)?\)`)
		var exprs []string
		for _, page := range []codegen.PageTemplateData{charge, fee} {
			exprs = append(exprs, call.FindAllString(renderPageInDirForTest(t, "pages", "list-page.tsx.tmpl", page), -1)...)
		}
		if len(exprs) < 3 {
			t.Fatalf("found %d money cell expressions, want at least 3: %v", len(exprs), exprs)
		}
		formatUtils, err := templates.FrontendTemplates().Get(filepath.Join("shared", "src", "lib", "format-utils.ts"))
		if err != nil {
			t.Fatalf("read shared/src/lib/format-utils.ts: %v", err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "format-utils.ts"), formatUtils, 0o644); err != nil {
			t.Fatal(err)
		}
		var harness strings.Builder
		harness.WriteString("import { formatMoneyCents } from \"./format-utils.ts\";\n")
		harness.WriteString("for (const currency of [\"USD\", \"EUR\"]) {\n")
		harness.WriteString("  const item = { amountCents: 1899n, tipCents: 250n, feeCents: 1899n, currency };\n")
		for _, e := range exprs {
			harness.WriteString("  console.log(currency + \"\\t" + e + "\\t\" + " + e + ");\n")
		}
		harness.WriteString("}\n")
		if err := os.WriteFile(filepath.Join(dir, "run.ts"), []byte(harness.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, filepath.Join(dir, "run.ts")).CombinedOutput()
		if err != nil {
			t.Fatalf("node run.ts: %v\n%s", err, out)
		}
		want := map[string]string{
			"USD\tformatMoneyCents(item.amountCents, item.currency)": "$18.99",
			"USD\tformatMoneyCents(item.tipCents, item.currency)":    "$2.50",
			"USD\tformatMoneyCents(item.feeCents)":                   "$18.99",
			"EUR\tformatMoneyCents(item.amountCents, item.currency)": "€18.99",
			"EUR\tformatMoneyCents(item.tipCents, item.currency)":    "€2.50",
			// No currency column: the row's currency is not consulted.
			"EUR\tformatMoneyCents(item.feeCents)": "$18.99",
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if i := strings.LastIndex(line, "\t"); i > 0 {
				got[line[:i]] = line[i+1:]
			}
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s renders %q, want %q\nnode output:\n%s", strings.ReplaceAll(k, "\t", " "), got[k], v, out)
			}
		}
	})
}
