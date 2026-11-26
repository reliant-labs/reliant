package generator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// existingServiceProto is a proto file the project already has that declares
// the service being scaffolded, outside forge's default
// proto/services/<svc>/v1/ layout: reliant keeps every service in
// proto/reliant/v1 (package reliant.v1). Scaffolding the service must then
// build service.go against that file, never write a second stub beside it
// (obs 1bc2d82d).
type existingServiceProto struct {
	// Rel is the file, relative to the project root.
	Rel string
	// ImportPath is the Go package below <module>/gen/, without its trailing
	// "/v1": what service.go.tmpl's ProtoImportPath names ("reliant").
	ImportPath string
	// GoPackage is the Go package name of the generated stubs ("reliantv1").
	GoPackage string
	// FileSymbol is protoc-gen-go's descriptor variable for the file
	// ("File_reliant_v1_pull_request_record_proto").
	FileSymbol string
}

var goPackageOptionRE = regexp.MustCompile(`(?m)^\s*option\s+go_package\s*=\s*"([^"]+)"\s*;`)

// findExistingServiceProto looks under <root>/proto for a .proto that declares
// `service <serviceGoName>` anywhere other than defaultRel. found is false
// when there is none, and when defaultRel itself declares it: forge's own
// layout is then the canonical contract. A file that declares it with a
// go_package the service template cannot import (not <module>/gen/<path>/v1)
// is an error that names the file, rather than a silent second stub, and so
// are two or more files declaring it: which one is the contract is the
// project's call, never the walk order's.
func findExistingServiceProto(root, modulePath, serviceGoName, defaultRel string) (existingServiceProto, bool, error) {
	protoRoot := filepath.Join(root, "proto")
	decl := regexp.MustCompile(`(?m)^\s*service\s+` + regexp.QuoteMeta(serviceGoName) + `\s*\{`)
	var hits []string
	var data []byte
	defaultDeclares := false
	err := filepath.WalkDir(protoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == protoRoot && os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			if path != protoRoot && (d.Name() == "forge" || strings.HasPrefix(d.Name(), ".")) && filepath.Dir(path) == protoRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil || !decl.Match(b) {
			return nil
		}
		if filepath.ToSlash(rel) == filepath.ToSlash(defaultRel) {
			defaultDeclares = true
			return nil
		}
		hits = append(hits, filepath.ToSlash(rel))
		data = b
		return nil
	})
	if err != nil {
		return existingServiceProto{}, false, err
	}
	if defaultDeclares || len(hits) == 0 {
		return existingServiceProto{}, false, nil
	}
	if len(hits) > 1 {
		return existingServiceProto{}, false, fmt.Errorf("service %s is declared in %d proto files (%s); keep it in one, then re-run", serviceGoName, len(hits), strings.Join(hits, ", "))
	}
	hit := existingServiceProto{Rel: hits[0]}
	m := goPackageOptionRE.FindSubmatch(data)
	if m == nil {
		return existingServiceProto{}, false, fmt.Errorf("%s declares service %s but has no go_package option; add `option go_package = \"%s/gen/<path>/v1;<pkg>v1\";` and re-run", hit.Rel, serviceGoName, modulePath)
	}
	importPath, goPkg, _ := strings.Cut(string(m[1]), ";")
	prefix := modulePath + "/gen/"
	below, ok := strings.CutPrefix(importPath, prefix)
	base, isV1 := strings.CutSuffix(below, "/v1")
	if !ok || !isV1 || base == "" {
		return existingServiceProto{}, false, fmt.Errorf("%s declares service %s with go_package %q; the service template imports %s<path>/v1, so scaffold the handler by hand or move the stubs there", hit.Rel, serviceGoName, string(m[1]), prefix)
	}
	if goPkg == "" {
		goPkg = filepath.Base(importPath)
	}
	hit.ImportPath, hit.GoPackage = base, goPkg
	protoRel := strings.TrimPrefix(hit.Rel, "proto/")
	hit.FileSymbol = "File_" + strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(protoRel)
	return hit, true, nil
}
