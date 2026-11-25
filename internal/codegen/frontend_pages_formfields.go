package codegen

// The create and edit forms render each control through one shared partial
// (`formField` in pages/_partials.tmpl) built from the installed component
// library — <Label>, <Input>, <Select>, <Textarea> from src/components/ui/ —
// rather than inline class strings. One partial, both forms, both frontend
// trees: restyling ui/input.tsx restyles every generated form, which is what
// makes the scaffold a starting point rather than a fork of the library.

// FormFieldView is one form control as the create and edit templates render
// it: the field, plus which form it sits on. The partial cannot take two
// arguments, and the one thing that differs between the forms — where a
// NOT NULL enum select starts — depends on which form it is.
type FormFieldView struct {
	PageField
	// OnCreate is true on the create form, where a NOT NULL enum select
	// starts on its column DEFAULT (EnumDefaultRef). The edit form starts on
	// the record's own value, loaded through useForm's `values`.
	OnCreate bool
}

// CreateFormFields are CreateFields as the create form's controls.
func (p PageTemplateData) CreateFormFields() []FormFieldView {
	return formFieldViews(p.CreateFields, true)
}

// UpdateFormFields are UpdateFields as the edit form's controls.
func (p PageTemplateData) UpdateFormFields() []FormFieldView {
	return formFieldViews(p.UpdateFields, false)
}

func formFieldViews(fields []PageField, onCreate bool) []FormFieldView {
	out := make([]FormFieldView, 0, len(fields))
	for _, f := range fields {
		out = append(out, FormFieldView{PageField: f, OnCreate: onCreate})
	}
	return out
}

// FormControls reports which library primitives a form renders, so the page
// imports exactly those: an unused import fails the generated frontend's
// lint on a file forge just wrote.
type FormControls struct {
	Label, Input, Select, Textarea bool
}

// CreateFormControls is FormControls for the create form.
func (p PageTemplateData) CreateFormControls() FormControls { return formControls(p.CreateFields) }

// UpdateFormControls is FormControls for the edit form.
func (p PageTemplateData) UpdateFormControls() FormControls { return formControls(p.UpdateFields) }

func formControls(fields []PageField) FormControls {
	var c FormControls
	for _, f := range fields {
		// Every control has a label, the foreign-key picker's included.
		c.Label = true
		if f.FK != nil {
			continue
		}
		switch f.Type {
		case "checkbox":
		case "select":
			c.Select = true
		case "base64", "textarea":
			c.Textarea = true
		default:
			c.Input = true
		}
	}
	return c
}
