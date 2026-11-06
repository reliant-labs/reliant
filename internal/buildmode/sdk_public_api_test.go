// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoregistry"
	"gopkg.in/yaml.v3"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	sdkv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// The public API is the rpcs listed in proto/public-api.txt, and nothing else.
// This file keeps four things saying the same sentence:
//
//   - proto/public-api.txt, the allowlist (modelled on control-plane's),
//   - buf.gen.sdk-go.yaml, whose inputs and M mappings decide which protos
//     are generated into sdk/go, and whose Connect plugin filters by `types:`,
//   - the generated tree in sdk/go/gen and gen/,
//   - the module boundary: sdk/go imports nothing from the app, and the app
//     reaches the SDK only by a published version, never a replace.
//
// Nothing becomes public by default: a service nobody classified fails here.

const (
	publicAPIFile = "proto/public-api.txt"
	sdkBufGen     = "buf.gen.sdk-go.yaml"
	protoDir      = "proto/reliant/v1"
	sdkPkgPath    = "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// neverPublic are rpcs that must not be exported however the file is edited:
// they decide who a run executes as, whether an automation runs at all, or
// mint authority. A rule of the product, not of this file, so a careless edit
// to public-api.txt cannot undo it. Wanting one of these public is an owner
// decision (token scopes), made here deliberately and in review.
var neverPublic = map[string]string{
	"TriggerService/EnableWorkflow":        "activation",
	"TriggerService/DisableWorkflow":       "activation",
	"TriggerService/SetTriggerEnabled":     "activation",
	"TriggerService/CreateTrigger":         "activation: chooses who a run executes as, and where",
	"TriggerService/UpdateTrigger":         "activation: chooses who a run executes as, and where",
	"TriggerService/DeleteTrigger":         "activation",
	"TriggerService/RotateWebhookToken":    "returns a secret once",
	"TriggerService/DraftAutomation":       "uses the caller's own model; a UI affordance",
	"ChatService/SetChatDaemon":            "daemon wiring",
	"ChatService/AdoptChat":                "daemon wiring",
	"ChatService/UpdateChatProviderPolicy": "billing wiring",
}

// neverPublicServices are whole services that must not be exported.
var neverPublicServices = map[string]string{
	"TokenService":           "a token never mints a token",
	"DaemonService":          "daemon-facing",
	"DaemonRegistryService":  "daemon-facing",
	"ToolsDaemonService":     "daemon-facing",
	"TerminalService":        "daemon-facing",
	"FileSystemService":      "daemon-facing",
	"BackgroundService":      "daemon-facing",
	"PackageCommandsService": "daemon-facing",
}

type verdict string

const (
	vExport   verdict = "export"
	vPartial  verdict = "partial"
	vWithhold verdict = "withhold"
)

type api struct {
	services map[string]verdict // service -> verdict
	rpcs     map[string]bool    // "Service/Rpc" exported by a partial service
}

func readAPI(t *testing.T, root string) api {
	t.Helper()
	f, err := os.Open(filepath.Join(root, publicAPIFile))
	if err != nil {
		t.Fatalf("open %s: %v", publicAPIFile, err)
	}
	defer func() { _ = f.Close() }()

	a := api{services: map[string]verdict{}, rpcs: map[string]bool{}}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// A typo'd verdict silently read as "withhold" would be harmless, but one
		// read as "export" would publish something nobody chose to.
		if len(fields) != 2 {
			t.Fatalf("%s:%d: want `<export|partial|withhold> <Service>` or `export <Service>/<Rpc>`, got %q", publicAPIFile, n, sc.Text())
		}
		v, name := verdict(fields[0]), fields[1]
		switch {
		case strings.Contains(name, "/"):
			if v != vExport {
				t.Fatalf("%s:%d: only `export` takes a Service/Rpc, got %q", publicAPIFile, n, sc.Text())
			}
			if a.rpcs[name] {
				t.Errorf("%s:%d: %s listed twice", publicAPIFile, n, name)
			}
			a.rpcs[name] = true
		case v == vExport || v == vPartial || v == vWithhold:
			if _, dup := a.services[name]; dup {
				t.Errorf("%s:%d: %s classified twice", publicAPIFile, n, name)
			}
			a.services[name] = v
		default:
			t.Fatalf("%s:%d: unknown verdict %q", publicAPIFile, n, fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return a
}

// protoFile is what the test needs of one .proto: where each service and rpc is
// declared, and what it imports.
type protoFile struct {
	name     string // "reliant/v1/run.proto"
	services map[string][]string
	imports  []string
}

var (
	serviceRe = regexp.MustCompile(`(?m)^service\s+(\w+)\s*\{`)
	rpcRe     = regexp.MustCompile(`(?m)^\s+rpc\s+(\w+)\s*\(`)
	importRe  = regexp.MustCompile(`(?m)^import\s+"([^"]+)"`)
)

func readProtos(t *testing.T, root string) map[string]*protoFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, protoDir, "*.proto"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no protos under %s (err=%v)", protoDir, err)
	}
	out := map[string]*protoFile{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		pf := &protoFile{name: "reliant/v1/" + filepath.Base(p), services: map[string][]string{}}
		for _, m := range importRe.FindAllStringSubmatch(src, -1) {
			pf.imports = append(pf.imports, m[1])
		}
		// Split the file at each `service X {` so an rpc belongs to its service.
		locs := serviceRe.FindAllStringSubmatchIndex(src, -1)
		for i, loc := range locs {
			name := src[loc[2]:loc[3]]
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			// A service body ends at its closing brace in column 0.
			body := src[loc[1]:end]
			if j := strings.Index(body, "\n}"); j >= 0 {
				body = body[:j]
			}
			for _, m := range rpcRe.FindAllStringSubmatch(body, -1) {
				pf.services[name] = append(pf.services[name], m[1])
			}
		}
		out[pf.name] = pf
	}
	return out
}

// publicRPCs is the set of "Service/Rpc" the allowlist exports.
func publicRPCs(a api, protos map[string]*protoFile) map[string]bool {
	out := map[string]bool{}
	for _, pf := range protos {
		for svc, rpcs := range pf.services {
			for _, rpc := range rpcs {
				switch a.services[svc] {
				case vExport:
					out[svc+"/"+rpc] = true
				case vPartial:
					if a.rpcs[svc+"/"+rpc] {
						out[svc+"/"+rpc] = true
					}
				}
			}
		}
	}
	return out
}

// publicFiles is every proto the public rpcs need: the files declaring a public
// service, and everything they import within reliant/v1 (a proto is a
// file-level unit, so a public message's definition travels even when its
// service does not).
func publicFiles(a api, protos map[string]*protoFile, public map[string]bool) map[string]bool {
	files := map[string]bool{}
	var add func(name string)
	add = func(name string) {
		if files[name] {
			return
		}
		pf := protos[name]
		if pf == nil {
			return // forge/v1, buf/validate, google/protobuf: not ours
		}
		files[name] = true
		for _, imp := range pf.imports {
			add(imp)
		}
	}
	for _, pf := range protos {
		for svc, rpcs := range pf.services {
			for _, rpc := range rpcs {
				if public[svc+"/"+rpc] {
					add(pf.name)
				}
			}
		}
	}
	return files
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPublicAPIClassifiesEveryService is the "nothing becomes public by
// default" half: every service has a verdict, every listed rpc exists, and the
// verdicts are coherent.
func TestPublicAPIClassifiesEveryService(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	a := readAPI(t, root)
	protos := readProtos(t, root)

	declared := map[string]map[string]bool{} // service -> rpcs
	for _, pf := range protos {
		for svc, rpcs := range pf.services {
			if declared[svc] != nil {
				t.Errorf("service %s declared in more than one file", svc)
			}
			declared[svc] = map[string]bool{}
			for _, r := range rpcs {
				declared[svc][r] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("parsed no services: the proto regexps no longer match, so nothing below is checked")
	}

	for svc := range declared {
		if _, ok := a.services[svc]; !ok {
			t.Errorf("%s is not classified in %s: add `export %s`, `partial %s` with its rpcs, or `withhold %s  # why`", svc, publicAPIFile, svc, svc, svc)
		}
	}
	for svc, v := range a.services {
		if declared[svc] == nil {
			t.Errorf("%s classifies %s, which no proto declares", publicAPIFile, svc)
		}
		if v == vPartial {
			n := 0
			for name := range a.rpcs {
				if strings.HasPrefix(name, svc+"/") {
					n++
				}
			}
			if n == 0 {
				t.Errorf("%s is `partial` but exports no rpc: use `withhold`", svc)
			}
		}
	}
	for name := range a.rpcs {
		svc, rpc, _ := strings.Cut(name, "/")
		switch {
		case declared[svc] == nil:
			t.Errorf("%s exports %s, but no proto declares service %s", publicAPIFile, name, svc)
		case !declared[svc][rpc]:
			t.Errorf("%s exports %s, but %s has no rpc %s", publicAPIFile, name, svc, rpc)
		case a.services[svc] != vPartial:
			t.Errorf("%s exports %s, but %s is `%s`: an rpc is listed only under a `partial` service", publicAPIFile, name, svc, a.services[svc])
		}
	}
}

// TestPublicAPINeverExportsWhatNeedsAPerson holds the product rule independent
// of the allowlist's contents.
func TestPublicAPINeverExportsWhatNeedsAPerson(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	a := readAPI(t, root)
	protos := readProtos(t, root)
	public := publicRPCs(a, protos)

	for rpc, why := range neverPublic {
		if public[rpc] {
			t.Errorf("%s is exported, and must not be (%s)", rpc, why)
		}
	}
	for svc, why := range neverPublicServices {
		if v := a.services[svc]; v == vExport || v == vPartial {
			t.Errorf("%s is `%s`, and must be withheld (%s)", svc, v, why)
		}
	}
	// And the other direction: the things the SDK promises are really there, so
	// the allowlist cannot be emptied without a test noticing.
	for _, want := range []string{
		"RunService/StartRun", "RunService/GetRun", "RunService/ListRuns", "RunService/SignalRun", "RunService/CancelRun",
		"RunService/PauseRun", "RunService/ResumeRun", "RunService/InterruptRun", "RunService/LastRunPerWorkflow",
		"TriggerService/ListTriggers", "TriggerService/GetTrigger", "TriggerService/FireTrigger",
		"TriggerService/ListTriggerEvents", "TriggerService/GetLaunchEvent",
	} {
		if !public[want] {
			t.Errorf("%s should be public and is not", want)
		}
	}
}

type bufPlugin struct {
	Local    []string `yaml:"local"`
	Out      string   `yaml:"out"`
	Opt      []string `yaml:"opt"`
	Types    []string `yaml:"types"`
	Strategy string   `yaml:"strategy"`
}

type bufTemplate struct {
	Inputs []struct {
		Directory string   `yaml:"directory"`
		Paths     []string `yaml:"paths"`
	} `yaml:"inputs"`
	Plugins []bufPlugin `yaml:"plugins"`
}

func readBufGen(t *testing.T, root, name string) bufTemplate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	var tmpl bufTemplate
	if err := yaml.Unmarshal(b, &tmpl); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tmpl
}

// goPlugin names the Go plugin an entry runs, or "" for any other plugin.
func goPlugin(p bufPlugin) string {
	if len(p.Local) == 0 {
		return ""
	}
	switch tool := p.Local[len(p.Local)-1]; tool {
	case "protoc-gen-go", "protoc-gen-connect-go":
		return tool
	}
	return ""
}

// mappedFiles are the protos an entry routes into the SDK through `M` options.
func mappedFiles(p bufPlugin) map[string]bool {
	out := map[string]bool{}
	for _, o := range p.Opt {
		if rest, ok := strings.CutPrefix(o, "M"); ok {
			file, to, _ := strings.Cut(rest, "=")
			if strings.HasPrefix(to, sdkPkgPath) {
				out[file] = true
			}
		}
	}
	return out
}

// TestBufGenMatchesThePublicAPI: buf.gen.sdk-go.yaml is where "public"
// becomes code. It generates exactly the public files, every Go plugin in it
// routes exactly those into the SDK (so its messages and clients agree on where
// a message lives), and its Connect plugin's `types:` are exactly the exported
// rpcs (so a withheld rpc has no client in the published module). The app's
// buf.gen.yaml routes exactly the same files into the SDK, so the app imports
// them from there and never generates a second copy.
func TestBufGenMatchesThePublicAPI(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	a := readAPI(t, root)
	protos := readProtos(t, root)
	public := publicRPCs(a, protos)
	files := publicFiles(a, protos, public)

	tmpl := readBufGen(t, root, sdkBufGen)
	inputs := map[string]bool{}
	for _, in := range tmpl.Inputs {
		for _, p := range in.Paths {
			inputs[strings.TrimPrefix(p, in.Directory+"/")] = true
		}
	}
	if got, want := sorted(inputs), sorted(files); !reflect.DeepEqual(got, want) {
		t.Errorf("%s generates %v, want exactly the public files %v", sdkBufGen, got, want)
	}

	var sdkConnect []bufPlugin
	for _, p := range tmpl.Plugins {
		tool := goPlugin(p)
		if tool == "" {
			continue
		}
		if p.Out != "sdk/go" {
			t.Errorf("%s: %s writes %q, want sdk/go (the SDK module)", sdkBufGen, tool, p.Out)
		}
		if got, want := sorted(mappedFiles(p)), sorted(files); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %s routes %v into the SDK, want exactly the public files %v", sdkBufGen, tool, got, want)
		}
		if tool == "protoc-gen-connect-go" {
			sdkConnect = append(sdkConnect, p)
		}
	}
	if len(sdkConnect) != 1 {
		t.Fatalf("%s has %d protoc-gen-connect-go entries, want exactly the one that writes the SDK's clients", sdkBufGen, len(sdkConnect))
	}
	if len(sdkConnect[0].Types) == 0 {
		t.Fatalf("%s: the SDK's protoc-gen-connect-go entry has no `types:`, so every rpc of the public files would get a client", sdkBufGen)
	}

	for _, p := range readBufGen(t, root, "buf.gen.yaml").Plugins {
		tool := goPlugin(p)
		if tool == "" {
			continue
		}
		if got, want := sorted(mappedFiles(p)), sorted(files); !reflect.DeepEqual(got, want) {
			t.Errorf("buf.gen.yaml: %s (out %q) routes %v into the SDK, want exactly the public files %v", tool, p.Out, got, want)
		}
		if tool == "protoc-gen-connect-go" && len(p.Types) > 0 {
			t.Errorf("buf.gen.yaml: the app's protoc-gen-connect-go entry filters by `types:`; the app's handlers need every rpc (the SDK's filtered clients are %s's)", sdkBufGen)
		}
	}

	// types: names a service (all of its rpcs) or service.method.
	got := map[string]bool{}
	for _, ty := range sdkConnect[0].Types {
		name, ok := strings.CutPrefix(ty, "reliant.v1.")
		if !ok {
			t.Fatalf("types entry %q is not in reliant.v1", ty)
		}
		if svc, rpc, isRPC := strings.Cut(name, "."); isRPC {
			got[svc+"/"+rpc] = true
			continue
		}
		for _, pf := range protos {
			for _, rpc := range pf.services[name] {
				got[name+"/"+rpc] = true
			}
		}
	}
	if !reflect.DeepEqual(sorted(got), sorted(public)) {
		t.Errorf("%s SDK connect `types:` selects\n  %v\nbut %s exports\n  %v", sdkBufGen, sorted(got), publicAPIFile, sorted(public))
	}
}

var procedureRe = regexp.MustCompile(`= "(/reliant\.v1\.\w+/\w+)"`)

// TestGeneratedTreeMatchesThePublicAPI checks what is actually on disk: the SDK
// holds the public protos and clients for exactly the public rpcs, and the app
// holds no second copy of what the SDK holds.
func TestGeneratedTreeMatchesThePublicAPI(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	a := readAPI(t, root)
	protos := readProtos(t, root)
	public := publicRPCs(a, protos)
	files := publicFiles(a, protos, public)

	base := func(f string) string { return strings.TrimSuffix(filepath.Base(f), ".proto") }

	// SDK messages: exactly the public files.
	sdkPB, _ := filepath.Glob(filepath.Join(root, "sdk/go/gen/reliant/v1/*.pb.go"))
	gotPB := map[string]bool{}
	for _, f := range sdkPB {
		gotPB[strings.TrimSuffix(filepath.Base(f), ".pb.go")] = true
	}
	wantPB := map[string]bool{}
	for f := range files {
		wantPB[base(f)] = true
	}
	if !reflect.DeepEqual(sorted(gotPB), sorted(wantPB)) {
		t.Errorf("sdk/go/gen/reliant/v1 holds %v, want the public files %v (run `task generate:forge`)", sorted(gotPB), sorted(wantPB))
	}

	// App messages: everything else, and none of the above. A file generated
	// into both trees registers its proto twice and panics every binary at init.
	appPB, _ := filepath.Glob(filepath.Join(root, "gen/reliant/v1/*.pb.go"))
	for _, f := range appPB {
		name := strings.TrimSuffix(filepath.Base(f), ".pb.go")
		if name == "sdk_alias" {
			continue // tools/sdkalias's re-export of the SDK's names, not a proto
		}
		if wantPB[name] {
			t.Errorf("gen/reliant/v1/%s.pb.go duplicates a public proto that sdk/go/gen already generates; the app must import the SDK's", name)
		}
	}
	for name := range protos {
		short := base(name)
		if wantPB[short] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "gen/reliant/v1", short+".pb.go")); err != nil {
			t.Errorf("%s is not public and has no gen/reliant/v1/%s.pb.go (run `task generate:forge`)", name, short)
		}
	}

	// SDK clients: exactly the public procedures, no more.
	gotProc := map[string]bool{}
	conns, _ := filepath.Glob(filepath.Join(root, "sdk/go/gen/reliant/v1/reliantv1connect/*.connect.go"))
	for _, f := range conns {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range procedureRe.FindAllStringSubmatch(string(b), -1) {
			p := strings.TrimPrefix(m[1], "/reliant.v1.")
			svc, rpc, _ := strings.Cut(p, "/")
			gotProc[svc+"/"+rpc] = true
		}
	}
	if len(gotProc) == 0 {
		t.Fatal("found no procedures in the SDK's generated clients: the check below would pass vacuously")
	}
	if !reflect.DeepEqual(sorted(gotProc), sorted(public)) {
		t.Errorf("the SDK's generated clients expose\n  %v\nbut %s exports\n  %v\n(run `task generate:forge`)", sorted(gotProc), publicAPIFile, sorted(public))
	}
}

// TestSDKHasOneRegistrationOfEachPublicProto is the init-panic guard, at
// runtime: this test binary links the app's generated package and the SDK's, so
// a double registration would have panicked before main. What is checked is that
// the type a caller gets from the app's import path IS the SDK's type, so a
// *Run crosses between the two without conversion.
func TestSDKHasOneRegistrationOfEachPublicProto(t *testing.T) {
	t.Parallel()

	var viaApp *reliantv1.Run = &sdkv1.Run{Id: "x"}
	if got := reflect.TypeOf(viaApp).Elem().PkgPath(); got != sdkPkgPath {
		t.Fatalf("reliantv1.Run lives in %q, want %q: the app has its own copy", got, sdkPkgPath)
	}
	if _, err := protoregistry.GlobalFiles.FindFileByPath("reliant/v1/run.proto"); err != nil {
		t.Fatalf("run.proto is not registered: %v", err)
	}
	// A proto that stays in the app resolves too, and refers to the SDK's types.
	fd, err := protoregistry.GlobalFiles.FindFileByPath("reliant/v1/inbox.proto")
	if err != nil {
		t.Fatalf("inbox.proto is not registered: %v", err)
	}
	if fd.Imports().Len() == 0 {
		t.Fatal("inbox.proto imports nothing, so it cannot show the app resolving the SDK's files")
	}
}

// TestSDKModuleIsSelfContained: sdk/go ships on its own, so it may import
// nothing from the app, and the app may not reach it through a replace that
// hides a missing release. A consumer ignores a dependency's replace
// directives, and `go install <module>@<version>` refuses a module that has
// one, so the app requires the SDK by a version the module proxy can serve
// (a pseudo-version of the main commit that holds it), never v0.0.0.
func TestSDKModuleIsSelfContained(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	mod, err := os.ReadFile(filepath.Join(root, "sdk/go/go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module github.com/reliant-labs/reliant/sdk/go\n") {
		t.Errorf("sdk/go/go.mod declares the wrong module:\n%s", strings.SplitN(string(mod), "\n", 2)[0])
	}
	if regexp.MustCompile(`(?m)^\s*github\.com/reliant-labs/reliant\s+v`).Match(mod) {
		t.Error("sdk/go/go.mod requires the app module: the SDK must not depend on what depends on it")
	}
	if regexp.MustCompile(`(?m)^\s*(replace\s+)?\S+\s+=>`).Match(mod) {
		t.Error("sdk/go/go.mod has a replace: a published module's go.mod must build on its own")
	}

	selfPrefix := "github.com/reliant-labs/reliant/sdk/go"
	appImport := regexp.MustCompile(`"(github\.com/reliant-labs/reliant(?:/[^"]*)?)"`)
	err = filepath.WalkDir(filepath.Join(root, "sdk/go"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range appImport.FindAllStringSubmatch(string(b), -1) {
			if imp := m[1]; imp != selfPrefix && !strings.HasPrefix(imp, selfPrefix+"/") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports %s: the SDK may import only itself", rel, imp)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^\s*(replace\s+)?github\.com/reliant-labs/reliant/sdk/go(\s+v\S+)?\s+=>`).Match(rootMod) {
		t.Error("go.mod replaces github.com/reliant-labs/reliant/sdk/go: consumers of this module ignore the replace and cannot resolve the SDK, and `go install @version` refuses it. Require a published version (go get github.com/reliant-labs/reliant/sdk/go@<main sha>)")
	}
	if regexp.MustCompile(`(?m)^\s*(require\s+)?github\.com/reliant-labs/reliant/sdk/go\s+v0\.0\.0(\s|$)`).Match(rootMod) {
		t.Error("go.mod requires github.com/reliant-labs/reliant/sdk/go v0.0.0, which no version provides")
	}

	// The license is Apache-2.0, in the module's own LICENSE.
	lic, err := os.ReadFile(filepath.Join(root, "sdk/go/LICENSE"))
	if err != nil {
		t.Fatalf("sdk/go has no LICENSE: %v", err)
	}
	if !strings.Contains(string(lic), "Apache License") || !strings.Contains(string(lic), "Version 2.0") {
		t.Error("sdk/go/LICENSE is not the Apache License, Version 2.0")
	}
}

// TestSDKNodeStatusSkippedMatchesTheProto: the SDK cannot name
// NodeExecutionStatus (streaming.proto is not public), so its follow engine
// carries the SKIPPED value as a constant. This is the other end of that
// agreement, read from the SDK's source (the app does not import the SDK): a
// renumbering in the proto must fail here, not turn every skipped node back
// into a green tick.
func TestSDKNodeStatusSkippedMatchesTheProto(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "sdk/go/reliant/follow/engine.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const NodeStatusSkipped = (\d+)$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("sdk/go/reliant/follow/engine.go no longer declares `const NodeStatusSkipped = <n>`")
	}
	got, _ := strconv.Atoi(string(m[1]))
	if want := int(reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED); got != want {
		t.Fatalf("follow.NodeStatusSkipped = %d, but the proto says SKIPPED is %d", got, want)
	}
}

// TestSDKSkippedStepActivityMatchesTheEngine: a skipped node records its step
// under the engine's model.ActivitySkippedStep activity name, and the SDK's
// Status reads that name (it has no import of the engine) to report SKIP.
func TestSDKSkippedStepActivityMatchesTheEngine(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "sdk/go/reliant/status.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const activitySkippedStep = "([^"]*)"$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("sdk/go/reliant/status.go no longer declares `const activitySkippedStep = \"...\"`")
	}
	if got := string(m[1]); got != model.ActivitySkippedStep {
		t.Fatalf("the SDK reads skipped steps as activity %q, but the engine records %q", got, model.ActivitySkippedStep)
	}
}
