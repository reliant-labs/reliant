package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// GeneratePlanProtoFile writes a populated proto file for a service based on
// plan RPC definitions and entity messages. It overwrites any existing proto
// stub at proto/services/<name>/v1/<name>.proto.
//
// When entities are provided, their message definitions are emitted inline
// in the service proto (before the RPC request/response messages) so that
// CRUD RPCs can reference them by bare name.
//
// When forge.yaml at `root` declares `api.rest: true`, CRUD-prefixed RPCs
// (Get/List/Create/Update/Delete<Entity>) are emitted with default
// `google.api.http` annotations so vanguard can transcode REST↔Connect at
// runtime. Non-CRUD RPCs are left unannotated and need the user to add
// annotations by hand. The `google/api/annotations.proto` import is added
// to the proto only when at least one CRUD RPC was annotated.
func GeneratePlanProtoFile(root, modulePath, serviceName string, rpcs []config.PlanRPC, entities []config.PlanEntity) error {
	protoDir := filepath.Join(root, "proto", "services", serviceName, "v1")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return fmt.Errorf("create proto directory: %w", err)
	}

	handlerName := naming.ToPascalCase(serviceName)
	needsTimestamp := planRPCsNeedTimestamp(rpcs) || planEntitiesNeedTimestamp(entities)
	entityImports := planRPCsEntityImports(rpcs)
	restEnabled := false
	if cfg, cerr := config.LoadProjectDir(root); cerr == nil {
		restEnabled = cfg.API.REST
	}

	// Determine which RPCs get HTTP annotations up-front so we know
	// whether to import google/api/annotations.proto.
	annotations := make(map[string]string, len(rpcs))
	if restEnabled {
		for _, rpc := range rpcs {
			if ann := crudHTTPAnnotation(rpc.Name); ann != "" {
				annotations[rpc.Name] = ann
			}
		}
	}
	needsHTTPAnnotation := len(annotations) > 0

	var b strings.Builder

	// Header
	b.WriteString("syntax = \"proto3\";\n\n")
	// Same package rule as the service scaffold, so the two writers of
	// proto/services/<svc>/v1/<svc>.proto cannot disagree.
	fmt.Fprintf(&b, "package %s;\n\n", ResolveServiceProtoPackage(root, serviceName).Package)
	fmt.Fprintf(&b, "option go_package = \"%s/gen/services/%s/v1;%sv1\";\n", modulePath, serviceName, serviceName)

	// Imports
	if needsTimestamp || len(entityImports) > 0 || needsHTTPAnnotation {
		b.WriteString("\n")
	}
	if needsTimestamp {
		b.WriteString("import \"google/protobuf/timestamp.proto\";\n")
	}
	if needsHTTPAnnotation {
		b.WriteString("import \"google/api/annotations.proto\";\n")
	}
	for _, imp := range entityImports {
		fmt.Fprintf(&b, "import \"%s\";\n", imp)
	}

	// Service block
	fmt.Fprintf(&b, "\n// %sService defines the %s service RPCs.\n", handlerName, serviceName)
	fmt.Fprintf(&b, "service %sService {\n", handlerName)
	for _, rpc := range rpcs {
		if rpc.Description != "" {
			fmt.Fprintf(&b, "  // %s\n", rpc.Description)
		}
		if ann, ok := annotations[rpc.Name]; ok {
			fmt.Fprintf(&b, "  rpc %s(%sRequest) returns (%sResponse) {\n", rpc.Name, rpc.Name, rpc.Name)
			fmt.Fprintf(&b, "    option (google.api.http) = %s;\n", ann)
			fmt.Fprintf(&b, "  }\n")
		} else {
			fmt.Fprintf(&b, "  rpc %s(%sRequest) returns (%sResponse) {}\n", rpc.Name, rpc.Name, rpc.Name)
		}
	}
	b.WriteString("}\n")

	// Entity messages (before RPC request/response messages)
	for _, ent := range entities {
		b.WriteString("\n")
		// Emit forge annotations as comments so the entity parser can
		// reconstruct metadata without proto options.
		fmt.Fprintf(&b, "// forge:entity\n")
		fmt.Fprintf(&b, "message %s {\n", ent.Name)
		fieldNum := 1

		// Always emit id as the first field unless the user already defined it.
		hasID := false
		for _, f := range ent.Fields {
			if f.Name == "id" {
				hasID = true
				break
			}
		}
		if !hasID {
			fmt.Fprintf(&b, "  string id = %d;\n", fieldNum)
			fieldNum++
		}

		for _, f := range ent.Fields {
			fmt.Fprintf(&b, "  %s %s = %d;\n", mapProtoType(f.Type), f.Name, fieldNum)
			fieldNum++
		}
		if ent.Timestamps {
			fmt.Fprintf(&b, "  google.protobuf.Timestamp created_at = %d;\n", fieldNum)
			fieldNum++
			fmt.Fprintf(&b, "  google.protobuf.Timestamp updated_at = %d;\n", fieldNum)
			fieldNum++
		}
		if ent.SoftDelete {
			fmt.Fprintf(&b, "  google.protobuf.Timestamp deleted_at = %d;\n", fieldNum)
			fieldNum++
		}
		b.WriteString("}\n")
	}

	// RPC Messages
	for _, rpc := range rpcs {
		b.WriteString("\n")
		fmt.Fprintf(&b, "message %sRequest {\n", rpc.Name)
		for i, f := range rpc.Request {
			fmt.Fprintf(&b, "  %s %s = %d;\n", mapProtoType(f.Type), f.Name, i+1)
		}
		b.WriteString("}\n")

		b.WriteString("\n")
		fmt.Fprintf(&b, "message %sResponse {\n", rpc.Name)
		for i, f := range rpc.Response {
			fmt.Fprintf(&b, "  %s %s = %d;\n", mapProtoType(f.Type), f.Name, i+1)
		}
		b.WriteString("}\n")
	}

	protoPath := filepath.Join(protoDir, fmt.Sprintf("%s.proto", serviceName))
	return atomicfile.WriteFile(protoPath, []byte(b.String()), 0644)
}

// mapProtoType converts a plan field type to a proto3 type string.
func mapProtoType(t string) string {
	if t == "timestamp" {
		return "google.protobuf.Timestamp"
	}
	// Everything else (string, int32, bool, repeated string,
	// google.protobuf.Timestamp, message Foo, etc.)
	// is used literally — proto validation will catch errors.
	return t
}

// planRPCsEntityImports collects db entity proto imports needed by the RPCs.
// It detects types like "db.v1.Patient" or "repeated db.v1.Patient" and returns
// the corresponding import paths (e.g. "db/v1/patient.proto").
func planRPCsEntityImports(rpcs []config.PlanRPC) []string {
	seen := map[string]bool{}
	var imports []string

	collect := func(t string) {
		// Strip "repeated " prefix if present.
		t = strings.TrimPrefix(t, "repeated ")
		if !strings.HasPrefix(t, "db.v1.") {
			return
		}
		// e.g. "db.v1.Patient" -> messageName = "Patient"
		msgName := strings.TrimPrefix(t, "db.v1.")
		snakeName := naming.ToSnakeCase(msgName)
		importPath := "db/v1/" + snakeName + ".proto"
		if !seen[importPath] {
			seen[importPath] = true
			imports = append(imports, importPath)
		}
	}

	for _, rpc := range rpcs {
		for _, f := range rpc.Request {
			collect(f.Type)
		}
		for _, f := range rpc.Response {
			collect(f.Type)
		}
	}
	return imports
}

// planRPCsNeedTimestamp reports whether any RPC field uses the timestamp type.
func planRPCsNeedTimestamp(rpcs []config.PlanRPC) bool {
	for _, rpc := range rpcs {
		for _, f := range rpc.Request {
			if f.Type == "timestamp" || f.Type == "google.protobuf.Timestamp" {
				return true
			}
		}
		for _, f := range rpc.Response {
			if f.Type == "timestamp" || f.Type == "google.protobuf.Timestamp" {
				return true
			}
		}
	}
	return false
}

// planEntitiesNeedTimestamp reports whether any entity uses timestamps or soft-delete
// (which require google.protobuf.Timestamp).
func planEntitiesNeedTimestamp(entities []config.PlanEntity) bool {
	for _, ent := range entities {
		if ent.Timestamps || ent.SoftDelete {
			return true
		}
		for _, f := range ent.Fields {
			if f.Type == "timestamp" || f.Type == "google.protobuf.Timestamp" {
				return true
			}
		}
	}
	return false
}

// crudHTTPAnnotation returns the `google.api.http` option body (the
// curly-brace block including braces) for a CRUD-prefixed RPC name, or
// the empty string if rpcName doesn't match a CRUD pattern. The mapping
// follows REST conventions:
//
//	Get<Entity>    → { get: "/v1/<entities>/{id}" }
//	List<Entity>   → { get: "/v1/<entities>" }                  (entity already plural)
//	Create<Entity> → { post: "/v1/<entities>" body: "*" }
//	Update<Entity> → { patch: "/v1/<entities>/{id}" body: "*" }
//	Delete<Entity> → { delete: "/v1/<entities>/{id}" }
//
// The entity name is lowercased and naïvely pluralized (append "s") so
// "Patient" → "/v1/patients". List<Entity> uses the suffix as-is on the
// assumption the user already named the RPC ListPatients (plural); we
// strip a single trailing "s" before lowercasing/pluralizing so list ends
// up identical to Get's collection path.
//
// Users who want richer pluralization (`ListPeople` → `/v1/people`) or
// nested resources should hand-edit the annotation after generation.
func crudHTTPAnnotation(rpcName string) string {
	switch {
	case strings.HasPrefix(rpcName, "Get"):
		ent := strings.TrimPrefix(rpcName, "Get")
		if ent == "" {
			return ""
		}
		return fmt.Sprintf(`{ get: "/v1/%s/{id}" }`, pluralizeForREST(ent))
	case strings.HasPrefix(rpcName, "List"):
		ent := strings.TrimPrefix(rpcName, "List")
		if ent == "" {
			return ""
		}
		// List<Plural> — normalise to singular then pluralise so the
		// collection path matches the Get<Singular> sibling.
		return fmt.Sprintf(`{ get: "/v1/%s" }`, pluralizeForREST(strings.TrimSuffix(ent, "s")))
	case strings.HasPrefix(rpcName, "Create"):
		ent := strings.TrimPrefix(rpcName, "Create")
		if ent == "" {
			return ""
		}
		return fmt.Sprintf(`{ post: "/v1/%s" body: "*" }`, pluralizeForREST(ent))
	case strings.HasPrefix(rpcName, "Update"):
		ent := strings.TrimPrefix(rpcName, "Update")
		if ent == "" {
			return ""
		}
		return fmt.Sprintf(`{ patch: "/v1/%s/{id}" body: "*" }`, pluralizeForREST(ent))
	case strings.HasPrefix(rpcName, "Delete"):
		ent := strings.TrimPrefix(rpcName, "Delete")
		if ent == "" {
			return ""
		}
		return fmt.Sprintf(`{ delete: "/v1/%s/{id}" }`, pluralizeForREST(ent))
	}
	return ""
}

// pluralizeForREST lowercases the entity name and appends "s" — the
// naïve pluralisation called out as acceptable. Users who want richer
// rules ("person" → "people") can hand-edit the emitted annotation
// post-generation; GeneratePlanProtoFile only writes during plan-mode
// emission, so subsequent `forge generate` runs won't clobber hand-edited
// .proto files.
func pluralizeForREST(entity string) string {
	return strings.ToLower(entity) + "s"
}
