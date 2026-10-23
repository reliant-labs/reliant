package codegen

// widgetAccessor is the generate-time RECONCILER: it injects the missing
// accessor into an existing lifecycle.go rather than rewriting the file. Its
// presence is what earns the scaffold-once template its exemption.
var widgetAccessor = template.Must(template.New("widgetAccessor").Parse(`
func (c *Components) Widget{{.FieldName}}() Widget {
return wrap("{{.Name}}", c.{{.FieldName}})
}
`))
