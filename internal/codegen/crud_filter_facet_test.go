package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The (forge.v1.filter) facets: a List request field that names another
// column and compares by range or prefix. growth hand-wrote three List
// handlers (a key prefix plus a week range, a published_after bound, a day
// range) because a generated List filter could only be `col = $1`.

// facetEntity is a snapshot table with a string key, a string week and a
// timestamp, the shapes the facets compare.
func facetEntity() EntityDef {
	return EntityDef{
		Name: "MetricSnapshot", TableName: "metric_snapshots", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "ID", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "key", GoName: "Key", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "week_start", GoName: "WeekStart", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "value", GoName: "Value", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
			{Name: "published", GoName: "Published", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "key", Type: "string", NotNull: true},
			{Name: "week_start", Type: "string", NotNull: true},
			{Name: "value", Type: "int64", NotNull: true},
			{Name: "published", Type: "bool", NotNull: true},
			{Name: "published_at", Type: "time"},
		},
		SearchColumns: []string{"key", "week_start"},
	}
}

func facetService(fields ...MessageFieldDef) ServiceDef {
	return ServiceDef{
		Name:       "MetricsService",
		GoPackage:  "example.com/test/gen/proto/services/metrics/v1",
		PkgName:    "metricsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListMetricSnapshots", InputType: "ListMetricSnapshotsRequest", OutputType: "ListMetricSnapshotsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListMetricSnapshotsRequest": append([]MessageFieldDef{
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
			}, fields...),
		},
	}
}

func TestGenerateCRUDHandlers_FilterFacets(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "metrics")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := "package metrics\n\nimport \"github.com/reliant-labs/forge/pkg/orm\"\n\ntype Deps struct {\n\tDB orm.Context\n}\n\ntype Service struct {\n\tdeps Deps\n}\n"
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := facetService(
		MessageFieldDef{Name: "key_prefix", ProtoType: "string", Filter: &ListFilterDef{Column: "key", Op: FilterOpPrefix}},
		MessageFieldDef{Name: "week_start_from", ProtoType: "string", Filter: &ListFilterDef{Column: "week_start", Op: FilterOpGTE}},
		MessageFieldDef{Name: "week_start_before", ProtoType: "string", IsOptional: true, Filter: &ListFilterDef{Column: "week_start", Op: FilterOpLT}},
		MessageFieldDef{Name: "published_after", ProtoType: "message", MessageType: "google.protobuf.Timestamp",
			Filter: &ListFilterDef{Column: "published_at", Op: FilterOpGTE}},
		MessageFieldDef{Name: "min_value", ProtoType: "int64", Filter: &ListFilterDef{Column: "value", Op: FilterOpGT}},
		MessageFieldDef{Name: "max_value", ProtoType: "int64", IsOptional: true, Filter: &ListFilterDef{Column: "value", Op: FilterOpLTE}},
		// An annotation with no op is the exact match, on the named column.
		MessageFieldDef{Name: "metric", ProtoType: "string", Filter: &ListFilterDef{Column: "key"}},
	)
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, []EntityDef{facetEntity()}), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"if req.KeyPrefix != \"\" {\n\t\t\t\topts = append(opts, orm.WherePrefix(\"key\", req.KeyPrefix))",
		"if req.WeekStartFrom != \"\" {\n\t\t\t\topts = append(opts, orm.WhereGte(\"week_start\", req.WeekStartFrom))",
		"if req.WeekStartBefore != nil {\n\t\t\t\topts = append(opts, orm.WhereLt(\"week_start\", *req.WeekStartBefore))",
		"if req.PublishedAfter != nil {\n\t\t\t\topts = append(opts, orm.WhereGte(\"published_at\", req.PublishedAfter.AsTime()))",
		"if req.MinValue != 0 {\n\t\t\t\topts = append(opts, orm.WhereGt(\"value\", req.MinValue))",
		"if req.MaxValue != nil {\n\t\t\t\topts = append(opts, orm.WhereLte(\"value\", *req.MaxValue))",
		"if req.Metric != \"\" {\n\t\t\t\topts = append(opts, orm.WhereEq(\"key\", req.Metric))",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("ops file lacks\n%s\n--- got:\n%s", want, content)
		}
	}
	// No facet field may leak through as an exact match on its own name.
	for _, phantom := range []string{`"key_prefix"`, `"week_start_from"`, `"published_after"`, `"min_value"`, `"metric"`} {
		if strings.Contains(content, phantom) {
			t.Errorf("ops file binds the phantom column %s", phantom)
		}
	}
}

func TestBuildCRUDTemplateData_FilterFacetErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field MessageFieldDef
		want  []string
	}{
		{
			name:  "column the entity does not declare",
			field: MessageFieldDef{Name: "posted_after", ProtoType: "string", Filter: &ListFilterDef{Column: "posted_at", Op: FilterOpGTE}},
			want:  []string{"posted_after", `"posted_at"`, "published_at"},
		},
		{
			name:  "prefix on a number",
			field: MessageFieldDef{Name: "value_prefix", ProtoType: "int64", Filter: &ListFilterDef{Column: "value", Op: FilterOpPrefix}},
			want:  []string{"value_prefix", "PREFIX", "string"},
		},
		{
			name:  "prefix on a non-text column",
			field: MessageFieldDef{Name: "at_prefix", ProtoType: "string", Filter: &ListFilterDef{Column: "published_at", Op: FilterOpPrefix}},
			want:  []string{"at_prefix", "published_at", "text"},
		},
		{
			name:  "range on a bool",
			field: MessageFieldDef{Name: "published_from", ProtoType: "bool", Filter: &ListFilterDef{Column: "published", Op: FilterOpGTE}},
			want:  []string{"published_from", "bool"},
		},
		{
			name:  "range on a message other than Timestamp",
			field: MessageFieldDef{Name: "window_from", ProtoType: "message", MessageType: "google.protobuf.Duration", Filter: &ListFilterDef{Column: "published_at", Op: FilterOpGTE}},
			want:  []string{"window_from", "google.protobuf.Duration"},
		},
		{
			name:  "Timestamp against a non-time column",
			field: MessageFieldDef{Name: "week_after", ProtoType: "message", MessageType: "google.protobuf.Timestamp", Filter: &ListFilterDef{Column: "week_start", Op: FilterOpGTE}},
			want:  []string{"week_after", "week_start", "time"},
		},
		// A facet on a reserved free-text name must not silently fall
		// back to the broad ILIKE search across every string column.
		{
			name:  "prefix facet on the reserved name search",
			field: MessageFieldDef{Name: "search", ProtoType: "string", Filter: &ListFilterDef{Column: "key", Op: FilterOpPrefix}},
			want:  []string{`"search"`, "reserved", "free-text"},
		},
		{
			name:  "range facet on the reserved name query",
			field: MessageFieldDef{Name: "query", ProtoType: "string", Filter: &ListFilterDef{Column: "week_start", Op: FilterOpGTE}},
			want:  []string{`"query"`, "reserved"},
		},
		{
			name:  "equality facet on the reserved name q",
			field: MessageFieldDef{Name: "q", ProtoType: "string", Filter: &ListFilterDef{Column: "key"}},
			want:  []string{`"q"`, "reserved"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := facetService(tc.field)
			_, err := buildCRUDTemplateData(svc, MatchCRUDMethods(svc, []EntityDef{facetEntity()}), nil, "example.com/test")
			if err == nil {
				t.Fatal("expected the generate to refuse the facet")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not mention %q: %v", w, err)
				}
			}
		})
	}
}
