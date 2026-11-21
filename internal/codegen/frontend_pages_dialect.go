package codegen

// FormDialect is the zod and @hookform/resolvers API a frontend's generated
// create/edit forms are written for. The zero value is what a frontend
// scaffolded today declares (zod 4, resolvers 7).
//
// A frontend's package.json is its own: forge writes it once and an upgrade
// never moves its pins. A form generated into a frontend that still declares
// zod 3 — a new entity added after forge itself moved to zod 4 — must keep
// compiling against that frontend's packages until its owner bumps them
// (`forge skill load migrations/v1.8.0`), so the dialect is read from the
// frontend, not assumed.
type FormDialect struct {
	// Zod3 writes the zod 3 spellings: z.nativeEnum for an enum select and
	// the .email() string method. zod 4 replaces both (z.enum takes a
	// TypeScript enum, z.email is a schema) and zod 3 has neither
	// replacement.
	Zod3 bool
	// LegacyResolver types the form by ONE type, z.infer of the schema. An
	// @hookform/resolvers below 5 returns a resolver generic over the form's
	// values, which a form typed by its input and output separately refuses.
	// From 5 on, zodResolver states both — what the inputs hold
	// (z.input, `unknown` for a coerced number) and what the schema turns
	// them into (z.output) — and a form typed by z.infer alone no longer
	// type-checks against it.
	LegacyResolver bool
}

// ApplyFormDialect writes page's form fields in dialect d: it records d for
// the page templates and re-projects every create/edit field's zod
// expression in it. Call it after AttachForeignKeys, which can make a field
// required and so change its expression.
func ApplyFormDialect(page *PageTemplateData, d FormDialect) {
	page.FormDialect = d
	for _, fields := range [][]PageField{page.CreateFields, page.UpdateFields} {
		for i := range fields {
			pf := &fields[i]
			pf.zod3 = d.Zod3
			if pf.chainRules != nil {
				pf.ZodChain = pf.chainRules.zodChain(pf.chainForm, pf.zod3)
			}
			attachZodExpr(pf)
		}
	}
}
