package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestGenerateDescriptor_StrategyAllMatchesPerDirectory pins that running
// protoc-gen-forge once over every proto (the descriptor template's
// `strategy: all`) writes the same gen/forge_descriptor/ tree, byte for byte,
// as buf's per-directory default did: one plugin run per directory. It holds
// because fragments are staged per proto file; a change that made the output
// depend on which files shared a plugin run would fail here.
//
// The paths are chosen so that sorting files and sorting directories
// disagree ("a-b/v1" sorts before "a/v1" as a file path, after it as a
// directory), and one directory holds two files.
func TestGenerateDescriptor_StrategyAllMatchesPerDirectory(t *testing.T) {
	t.Parallel()

	files := []*descriptorpb.FileDescriptorProto{
		descriptorTestFile("a-b/v1/x.proto", "XService"),
		descriptorTestFile("a/v1/a.proto", "AService"),
		descriptorTestFile("a/v1/b.proto", "BService"),
		descriptorTestFile("b/v1/y.proto", "YService"),
		descriptorTestFile("z/v1/z.proto", "ZService"),
	}

	// strategy: all — one plugin run over every file.
	allWork, allProject := t.TempDir(), t.TempDir()
	runDescriptorPlugin(t, files, nil, allWork)

	// strategy: directory — one plugin run per directory, as buf grouped them.
	perDirWork, perDirProject := t.TempDir(), t.TempDir()
	byDir := map[string][]string{}
	var dirs []string
	for _, f := range files {
		dir := path.Dir(f.GetName())
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], f.GetName())
	}
	for _, dir := range dirs {
		runDescriptorPlugin(t, files, byDir[dir], perDirWork)
	}

	if err := MergeDescriptorFragments(allWork, allProject); err != nil {
		t.Fatalf("MergeDescriptorFragments (strategy all): %v", err)
	}
	if err := MergeDescriptorFragments(perDirWork, perDirProject); err != nil {
		t.Fatalf("MergeDescriptorFragments (per directory): %v", err)
	}
	all := readDescriptorTree(t, allProject)
	perDir := readDescriptorTree(t, perDirProject)
	if len(all) != len(files) {
		t.Fatalf("strategy all wrote %d descriptor files, want %d: %v", len(all), len(files), all)
	}
	for _, f := range files {
		rel := codegen.DescriptorFileFor(f.GetName())
		if !bytes.Equal([]byte(all[rel]), []byte(perDir[rel])) {
			t.Errorf("%s differs between one plugin run and one per directory\n--- strategy all ---\n%s\n--- per directory ---\n%s", rel, all[rel], perDir[rel])
		}
	}
	if len(perDir) != len(all) {
		t.Errorf("per-directory runs wrote %d descriptor files, strategy all %d", len(perDir), len(all))
	}
}

// readDescriptorTree returns every file under projectDir's descriptor
// directory, keyed by its project-relative slash path.
func readDescriptorTree(t *testing.T, projectDir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(filepath.Join(projectDir, codegen.DescriptorDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(projectDir, p)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", codegen.DescriptorDir, err)
	}
	return got
}

// runDescriptorPlugin runs generateDescriptor as buf would: every file is in
// the request, and generate (nil: all of them) names the ones this run
// generates.
func runDescriptorPlugin(t *testing.T, files []*descriptorpb.FileDescriptorProto, generate []string, out string) {
	t.Helper()
	if generate == nil {
		for _, f := range files {
			generate = append(generate, f.GetName())
		}
	}
	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{FileToGenerate: generate, ProtoFile: files})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	if err := generateDescriptor(p, out); err != nil {
		t.Fatalf("generateDescriptor(%v): %v", generate, err)
	}
}

// descriptorTestFile is a self-contained proto file at name declaring one
// service with one rpc, in a package derived from its directory (so files
// sharing a directory share a package, as buf requires).
func descriptorTestFile(name, service string) *descriptorpb.FileDescriptorProto {
	pkg := strings.NewReplacer("/", ".", "-", "_").Replace(path.Dir(name))
	goPkg := strings.ReplaceAll(pkg, ".", "")
	msg := service + "Message"
	empty := "." + pkg + "." + msg
	return &descriptorpb.FileDescriptorProto{
		Name:        proto.String(name),
		Package:     proto.String(pkg),
		Syntax:      proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String(fmt.Sprintf("example.com/gen/%s;%s", path.Dir(name), goPkg))},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String(msg)}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String(service),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Do"),
				InputType:  proto.String(empty),
				OutputType: proto.String(empty),
			}},
		}},
	}
}
