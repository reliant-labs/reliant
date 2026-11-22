package codegen

import (
	"strings"
	"testing"
)

func pageFieldNamed(t *testing.T, fields []PageField, name string) PageField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no form field %q", name)
	return PageField{}
}

// TestApplyFormDialect_Zod3 pins what a frontend that still declares zod 3
// gets: the zod 3 spellings, re-projected for every create and edit field.
// zod 3 has neither z.enum over a TypeScript enum nor z.email(), so a page
// written in zod 4 would not compile there.
func TestApplyFormDialect_Zod3(t *testing.T) {
	page := fieldRulesPage(t, fieldRulesSvc())
	ApplyFormDialect(&page, FormDialect{Zod3: true, LegacyResolver: true})

	for form, fields := range map[string][]PageField{"create": page.CreateFields, "edit": page.UpdateFields} {
		if got, want := pageFieldNamed(t, fields, "kind").ZodExpr,
			`z.coerce.number().pipe(z.nativeEnum(ThingKind)).refine((v): boolean => v !== ThingKind.UNSPECIFIED, "Choose a value")`; got != want {
			t.Errorf("%s kind zod = %s, want %s", form, got, want)
		}
		if got, want := pageFieldNamed(t, fields, "email").ZodExpr,
			`z.preprocess((v) => (v === "" ? undefined : v), z.string().email("Enter a valid email address").optional())`; got != want {
			t.Errorf("%s email zod = %s, want %s", form, got, want)
		}
		// A rule that is the same in both dialects is left as it was.
		if got, want := pageFieldNamed(t, fields, "name").ZodExpr, `z.string().min(1, "Required")`; got != want {
			t.Errorf("%s name zod = %s, want %s", form, got, want)
		}
	}

	// Back to zod 4: the dialect is a projection, not a one-way rewrite.
	ApplyFormDialect(&page, FormDialect{})
	if got := pageFieldNamed(t, page.CreateFields, "email").ZodExpr; !strings.Contains(got, `.pipe(z.email("Enter a valid email address"))`) {
		t.Errorf("zod 4 email zod = %s, want the z.email() pipe", got)
	}
}

// TestFormPages_ResolverTyping pins how the create and edit forms are typed
// for each @hookform/resolvers generation. From 5 on, zodResolver states the
// form's input (z.input) and its parsed output (z.output) separately, and a
// form typed by z.infer alone fails tsc wherever the two differ (a coerced
// number's input is `unknown`). Below 5 the resolver is generic over one
// type, so the form keeps the single z.infer type.
func TestFormPages_ResolverTyping(t *testing.T) {
	page := enumPageForTest(t)
	legacy := page
	legacy.CreateFields = append([]PageField(nil), page.CreateFields...)
	legacy.UpdateFields = append([]PageField(nil), page.UpdateFields...)
	ApplyFormDialect(&legacy, FormDialect{Zod3: true, LegacyResolver: true})

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"create-page.tsx.tmpl", "edit-page.tsx.tmpl"} {
			current := renderPageTemplate(t, kind, name, page)
			for _, want := range []string{
				"});\n\ntype FormInput = z.input<typeof schema>;\ntype FormValues = z.output<typeof schema>;\n\n",
				"useForm<FormInput, unknown, FormValues>({",
				"z.enum(BrandStatus)",
			} {
				if !strings.Contains(current, want) {
					t.Errorf("%s/%s (zod 4) missing %q:\n%s", kind, name, want, current)
				}
			}
			if strings.Contains(current, "z.infer<") || strings.Contains(current, "nativeEnum") {
				t.Errorf("%s/%s (zod 4) still uses a zod 3 spelling:\n%s", kind, name, current)
			}

			old := renderPageTemplate(t, kind, name, legacy)
			for _, want := range []string{
				"});\n\ntype FormValues = z.infer<typeof schema>;\n\n",
				"useForm<FormValues>({",
				"z.nativeEnum(BrandStatus)",
			} {
				if !strings.Contains(old, want) {
					t.Errorf("%s/%s (zod 3) missing %q:\n%s", kind, name, want, old)
				}
			}
			if strings.Contains(old, "FormInput") {
				t.Errorf("%s/%s (zod 3) types the form by its input:\n%s", kind, name, old)
			}
		}
	}
}
