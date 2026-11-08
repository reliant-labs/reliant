package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The merge invariant for the forge descriptor: two branches that change
// DIFFERENT protos merge with plain `git merge` — no merge driver and no
// attributes, exactly what GitHub runs server-side — because the descriptor
// is not in git at all, and the descriptor a reader gets after the merge
// (codegen.LoadDescriptor, extracting on demand) is byte-for-byte what a fresh
// generate writes for the merged protos.
//
// Before, the descriptor was one committed aggregate file
// (gen/forge_descriptor.json, 1.2 MB in reliant) that every proto change
// rewrote, so every such pair of pull requests conflicted on it.
//
// "Generate" here is the real descriptor path minus buf's .proto parser: the
// protoc plugin entry point (generateDescriptor, driven through protogen) over
// compiled descriptors of the same protos written to disk, then the parent's
// write (MergeDescriptorFragments → codegen.WriteDescriptor), which stamps
// every file from the .proto bytes on disk.

// descProto is one proto file of the fixture: a service and its rpcs.
type descProto struct {
	name string // lower-case; the file is proto/services/<name>/v1/<name>.proto
	rpcs []string
}

func (p descProto) rel() string { return "services/" + p.name + "/v1/" + p.name + ".proto" }

func (p descProto) service() string { return strings.ToUpper(p.name[:1]) + p.name[1:] + "Service" }

// text renders the .proto source. Its bytes are what the stamp hashes, so it
// must change exactly when the compiled descriptor does.
func (p descProto) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "syntax = \"proto3\";\n\npackage %s.v1;\n\noption go_package = \"example.com/app/gen/services/%s/v1;%sv1\";\n\nmessage Empty {}\n\nservice %s {\n", p.name, p.name, p.name, p.service())
	for _, r := range p.rpcs {
		fmt.Fprintf(&b, "  rpc %s(Empty) returns (Empty);\n", r)
	}
	b.WriteString("}\n")
	return b.String()
}

func (p descProto) compiled() *descriptorpb.FileDescriptorProto {
	pkg := p.name + ".v1"
	svc := &descriptorpb.ServiceDescriptorProto{Name: proto.String(p.service())}
	for _, r := range p.rpcs {
		svc.Method = append(svc.Method, &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(r),
			InputType:  proto.String("." + pkg + ".Empty"),
			OutputType: proto.String("." + pkg + ".Empty"),
		})
	}
	return &descriptorpb.FileDescriptorProto{
		Name:        proto.String(p.rel()),
		Package:     proto.String(pkg),
		Syntax:      proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.com/app/gen/services/" + p.name + "/v1;" + p.name + "v1")},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Empty")}},
		Service:     []*descriptorpb.ServiceDescriptorProto{svc},
	}
}

// descTree is the project's protos by name.
type descTree map[string]descProto

func (tr descTree) clone() descTree {
	out := descTree{}
	for k, v := range tr {
		v.rpcs = append([]string(nil), v.rpcs...)
		out[k] = v
	}
	return out
}

// writeAndGenerate replaces the project's proto tree with tr and generates the
// descriptor from it.
func (tr descTree) writeAndGenerate(t *testing.T, root string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(root, "proto")); err != nil {
		t.Fatal(err)
	}
	for _, p := range tr {
		writeTreeFileT(t, filepath.Join(root, "proto", filepath.FromSlash(p.rel())), p.text())
	}
	generateDescriptorFromProtos(t, root)
}

// generateDescriptorFromProtos is generate's descriptor step over the tree
// currently on disk: it reads which protos exist from proto/, compiles them,
// and runs the plugin and the parent's write.
func generateDescriptorFromProtos(t *testing.T, root string) {
	t.Helper()
	var files []*descriptorpb.FileDescriptorProto
	var names []string
	entries, _ := filepath.Glob(filepath.Join(root, "proto", "services", "*", "v1", "*.proto"))
	sort.Strings(entries)
	for _, e := range entries {
		data, err := os.ReadFile(e)
		if err != nil {
			t.Fatal(err)
		}
		p := parseDescProto(t, filepath.Base(filepath.Dir(filepath.Dir(e))), string(data))
		files = append(files, p.compiled())
		names = append(names, p.rel())
	}
	plugin, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{FileToGenerate: names, ProtoFile: files})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	work := t.TempDir()
	if err := generateDescriptor(plugin, work); err != nil {
		t.Fatalf("generateDescriptor: %v", err)
	}
	if err := MergeDescriptorFragments(work, root); err != nil {
		t.Fatalf("MergeDescriptorFragments: %v", err)
	}
}

// parseDescProto reads back the rpc list descProto.text rendered, so a merged
// work tree is compiled from what git actually merged.
func parseDescProto(t *testing.T, name, text string) descProto {
	t.Helper()
	p := descProto{name: name}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "rpc" {
			p.rpcs = append(p.rpcs, strings.SplitN(f[1], "(", 2)[0])
		}
	}
	if p.text() != text {
		t.Fatalf("proto %s does not round-trip; the merge produced a proto this fixture cannot compile:\n%s", name, text)
	}
	return p
}

// snapshotDir returns every file under dir by relative path.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// plainGit runs git with no system or global config: no merge driver, no
// attributes beyond the repository's own (it has none) — what GitHub runs.
func plainGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustPlainGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := plainGit(t, dir, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// descEdit is one branch's change to the proto tree.
type descEdit struct {
	label string
	touch string // the proto it changes; two edits are independent when these differ
	apply func(descTree)
}

func addRPC(name, rpc string) descEdit {
	return descEdit{"add " + rpc + " to " + name, name, func(tr descTree) {
		p := tr[name]
		p.rpcs = append(p.rpcs, rpc)
		tr[name] = p
	}}
}

func addProto(name string) descEdit {
	return descEdit{"add proto " + name, name, func(tr descTree) {
		tr[name] = descProto{name: name, rpcs: []string{"Ping"}}
	}}
}

func deleteProto(name string) descEdit {
	return descEdit{"delete proto " + name, name, func(tr descTree) { delete(tr, name) }}
}

func TestDescriptorMergeProperty_IndependentProtoChangesMergeWithPlainGit(t *testing.T) {
	if testing.Short() {
		t.Skip("runs ~100 real git commands across every pair of independent proto edits (~3s); runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := descTree{}
	for _, n := range []string{"alpha", "bravo", "charlie", "delta"} {
		base[n] = descProto{name: n, rpcs: []string{"Get", "List"}}
	}
	edits := []descEdit{
		addRPC("alpha", "Create"),
		addRPC("bravo", "Delete"),
		addRPC("charlie", "Update"),
		addProto("echo"),
		addProto("foxtrot"),
		deleteProto("delta"),
	}

	for i, a := range edits {
		for _, b := range edits[i+1:] {
			if a.touch == b.touch {
				continue
			}
			t.Run(a.label+" | "+b.label, func(t *testing.T) {
				// The forge binary registers buf's extraction; this registers
				// the same plugin path over compiled descriptors.
				codegen.RegisterDescriptorBuilder(func(root string) error {
					generateDescriptorFromProtos(t, root)
					return nil
				})
				t.Cleanup(func() { codegen.RegisterDescriptorBuilder(nil) })
				repo := t.TempDir()
				mustPlainGit(t, repo, "init", "-q", "-b", "main")
				writeTreeFileT(t, filepath.Join(repo, ".gitignore"), descriptorIgnoreBlock)
				base.writeAndGenerate(t, repo)
				mustPlainGit(t, repo, "add", "-A")
				mustPlainGit(t, repo, "commit", "-qm", "base")
				if tracked, _ := plainGit(t, repo, "ls-files", codegen.DescriptorDir); strings.TrimSpace(tracked) != "" {
					t.Fatalf("the descriptor was committed:\n%s", tracked)
				}

				for _, side := range []struct {
					branch string
					edit   descEdit
				}{{"feature", a}, {"main", b}} {
					if side.branch == "feature" {
						mustPlainGit(t, repo, "checkout", "-qb", "feature")
					} else {
						mustPlainGit(t, repo, "checkout", "-q", "main")
					}
					tr := base.clone()
					side.edit.apply(tr)
					tr.writeAndGenerate(t, repo)
					mustPlainGit(t, repo, "add", "-A")
					mustPlainGit(t, repo, "commit", "-qm", side.edit.label)
				}

				if out, err := plainGit(t, repo, "merge", "--no-edit", "feature"); err != nil {
					t.Fatalf("plain git merge conflicted on two independent proto changes:\n%s", out)
				}
				if status, _ := plainGit(t, repo, "status", "--porcelain"); strings.TrimSpace(status) != "" {
					t.Fatalf("the merge left the tree dirty:\n%s", status)
				}

				// A reader after the merge: the work tree still holds main's
				// descriptor, stale for the merged protos, so LoadDescriptor
				// must extract it again.
				if _, err := codegen.LoadDescriptor(repo); err != nil {
					t.Fatalf("the merged checkout's descriptor does not load: %v", err)
				}
				merged := snapshotDir(t, filepath.Join(repo, codegen.DescriptorDir))

				if err := os.RemoveAll(filepath.Join(repo, codegen.DescriptorDir)); err != nil {
					t.Fatal(err)
				}
				generateDescriptorFromProtos(t, repo)
				fresh := snapshotDir(t, filepath.Join(repo, codegen.DescriptorDir))
				if len(fresh) == 0 {
					t.Fatal("generate wrote no descriptor files")
				}
				for rel, body := range fresh {
					if merged[rel] != body {
						t.Errorf("%s: the merge differs from a fresh generate\nmerged:\n%s\nfresh:\n%s", rel, merged[rel], body)
					}
				}
				for rel := range merged {
					if _, ok := fresh[rel]; !ok {
						t.Errorf("%s: the reader kept a file a fresh generate does not write", rel)
					}
				}
			})
		}
	}
}
