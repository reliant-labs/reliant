package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// Human-facing copy for a multi-word entity must be word-split
// ("retained databases"), while identifiers and routes keep the PascalCase /
// slug forms.
func TestFrontendPageHumanLabelsMultiWordEntity(t *testing.T) {
	data := map[string]any{
		"EntityName":         "RetainedDatabase",
		"EntityNamePlural":   "RetainedDatabases",
		"EntitySlug":         "retained-databases",
		"ServiceName":        "RetainedDatabaseService",
		"ServiceNameCamel":   "retainedDatabaseService",
		"HooksImportPath":    "@/hooks/hooks",
		"TypesImportPath":    "@/gen/x_pb",
		"ListRPC":            "ListRetainedDatabases",
		"GetRPC":             "GetRetainedDatabase",
		"CreateRPC":          "CreateRetainedDatabase",
		"UpdateRPC":          "UpdateRetainedDatabase",
		"DeleteRPC":          "DeleteRetainedDatabase",
		"HasList":            true,
		"SearchFilterField":  "q",
		"HasGet":             true,
		"HasCreate":          true,
		"HasUpdate":          true,
		"HasDelete":          true,
		"CreateFields":       []any{},
		"UpdateFields":       []any{},
		"ListResponseType":   "ListRetainedDatabasesResponse",
		"GetResponseType":    "GetRetainedDatabaseResponse",
		"CreateRequestType":  "CreateRetainedDatabaseRequest",
		"CreateResponseType": "CreateRetainedDatabaseResponse",
		"UpdateRequestType":  "UpdateRetainedDatabaseRequest",
		"GetRequestType":     "GetRetainedDatabaseRequest",
		"DeleteRequestType":  "DeleteRetainedDatabaseRequest",
	}
	cases := []struct {
		dir, page string
		want      []string
	}{
		{"pages", "list-page.tsx.tmpl", []string{`emptyTitle="No retained databases found"`, `title="Retained Databases"`, `Search retained databases…`, `Create Retained Database`}},
		{"vite-spa-pages", "list-page.tsx.tmpl", []string{`emptyTitle="No retained databases found"`, `title="Retained Databases"`, `Create Retained Database`}},
		{"pages", "create-page.tsx.tmpl", []string{`title="Create Retained Database"`, `label: "Retained Databases", href: "/retained-databases"`}},
		{"pages", "edit-page.tsx.tmpl", []string{`title="Edit Retained Database"`, `Back to retained databases`}},
		{"pages", "detail-page.tsx.tmpl", []string{`title="Delete Retained Database"`, `delete this retained database?`}},
	}
	for _, c := range cases {
		t.Run(c.dir+"/"+c.page, func(t *testing.T) {
			out, err := FrontendTemplates().Render(filepath.Join(c.dir, c.page), data)
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			for _, w := range c.want {
				if !strings.Contains(s, w) {
					t.Errorf("missing %q", w)
				}
			}
			if strings.Contains(s, "retaineddatabase") {
				t.Errorf("run-together lowercase leaked into output")
			}
		})
	}
}
