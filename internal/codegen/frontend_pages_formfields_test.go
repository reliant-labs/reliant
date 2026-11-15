package codegen

import (
	"strings"
	"testing"
)

// TestFormFields_ErrorsAreWiredToTheirControls pins the form a11y contract:
// each control is the installed ui/ primitive, carries aria-invalid and an
// aria-describedby naming its error message's id, and a required one is
// aria-required (the visual asterisk on <Label required> is aria-hidden).
// Before, a failed submit left aria-invalid and aria-describedby null, and a
// screen reader read the field without the reason it was refused.
func TestFormFields_ErrorsAreWiredToTheirControls(t *testing.T) {
	page := PageTemplateData{
		EntityName: "Customer", EntityNamePlural: "Customers", EntitySlug: "customers",
		HasCreate: true, HasUpdate: true, HasGet: true,
		CreateRPC: "CreateCustomer", UpdateRPC: "UpdateCustomer", GetRPC: "GetCustomer",
		HooksImportPath: "@/hooks/crm-service-hooks", PkFieldCamel: "id",
		CreateFields: []PageField{
			{Name: "name", ProtoName: "name", Label: "Name", Type: "text", TSType: "string", Required: true, ZodExpr: `z.string().min(1, "Required")`},
			{Name: "email", ProtoName: "email", Label: "Email", Type: "email", TSType: "string", ZodExpr: `z.string().email("Enter a valid email address")`},
		},
	}
	page.UpdateFields = page.CreateFields
	for _, kind := range []string{"pages", "vite-spa-pages"} {
		for _, name := range []string{"create-page.tsx.tmpl", "edit-page.tsx.tmpl"} {
			out := renderPageTemplate(t, kind, name, page)
			for _, want := range []string{
				`import Input from "@/components/ui/input";`,
				`import Label from "@/components/ui/label";`,
				`import Button from "@/components/ui/button";`,
				`<Label htmlFor="name" required>`,
				`<Input`,
				`aria-invalid={errors.name ? true : undefined}`,
				`aria-describedby={errors.name ? "name-error" : undefined}`,
				`<p id="name-error"`,
				`aria-describedby={errors.email ? "email-error" : undefined}`,
				`<Button type="submit"`,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%s/%s missing %q:\n%s", kind, name, want, out)
				}
			}
			if strings.Contains(out, `<input`) {
				t.Errorf("%s/%s still hand-rolls an <input> for a text field:\n%s", kind, name, out)
			}
			if strings.Contains(out, `import Select from`) || strings.Contains(out, `import Textarea from`) {
				t.Errorf("%s/%s imports a primitive it never renders (an eslint error on a fresh scaffold):\n%s", kind, name, out)
			}
		}
	}
}
