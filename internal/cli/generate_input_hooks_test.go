package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func TestGenerateInputHooksPhaseSelection(t *testing.T) {
	ctx := &pipelineContext{Cfg: &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{
		{Name: "legacy"}, {Name: "vendor", Phase: "input"}, {Name: "explicit", Phase: "post"}, {Name: "second", Phase: "input"},
	}}}}
	for phase, want := range map[string][]string{"input": {"vendor", "second"}, "post": {"legacy", "explicit"}} {
		var got []string
		for _, h := range generateHooksForPhase(ctx, phase) {
			got = append(got, h.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v != %v", phase, got, want)
		}
	}
	ctx.Cfg.Generate.Hooks[0].Writes = []string{"post-output"}
	ctx.Cfg.Generate.Hooks[1].Writes = []string{"input-output"}
	if got := writesGenerateInputHooks(ctx); !reflect.DeepEqual(got, []string{"input-output"}) {
		t.Fatalf("input writes: %v", got)
	}
	if got := writesGeneratePostHooks(ctx); !reflect.DeepEqual(got, []string{"post-output"}) {
		t.Fatalf("post writes: %v", got)
	}
	index := map[string]int{}
	for i, s := range generateSteps() {
		index[s.Name] = i
	}
	for _, consumer := range []string{"frontend hooks", "frontend mocks + transport", "cleanup stale codegen", generateHooksStep} {
		if index[generateInputHooksStep] >= index[consumer] {
			t.Errorf("input hooks must precede %s", consumer)
		}
	}
}

func TestGenerateInputHooksFailureRestoresDeclaredWrites(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "descriptor.txt"), "original")
	load := GenStep{Name: "load project config", Gate: always, ReadOnly: true, Run: func(ctx *pipelineContext) error {
		h := helperHook("vendor", "", "write", "descriptor.txt", "fail")
		h.Phase = "input"
		h.Writes = []string{"descriptor.txt"}
		ctx.Cfg = &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{h}}}
		return nil
	}}
	consumed := false
	withPipelineSteps(t, []GenStep{load, productionStep(t, generateInputHooksStep), {Name: "consumer", Gate: always, Run: func(*pipelineContext) error { consumed = true; return nil }}})
	if err := runGeneratePipelineFlags(root, pipelineFlags{}); err == nil {
		t.Fatal("failed input hook succeeded")
	}
	if consumed {
		t.Fatal("consumer ran after input failure")
	}
	got, err := os.ReadFile(filepath.Join(root, "descriptor.txt"))
	if err != nil || string(got) != "original" {
		t.Fatalf("input not restored: %q, %v", got, err)
	}
}

// The input hook replaces a vendored descriptor BEFORE the mock projection.
// Use the real emitter and global sweep, without git tracking or manual cleanup.
func TestGenerateInputHooksRemovedServiceConverges(t *testing.T) {
	t.Setenv("FORGE_GENERATE_HOOK_HELPER", "1")
	for _, ownership := range []string{"pristine", "edited", "disowned"} {
		t.Run(ownership, func(t *testing.T) {
			resetCleanupRunState(t)
			t.Cleanup(func() { checksums.ResetSkipWrite() })
			root := t.TempDir()
			const fe = "frontends/web"
			const stub = fe + "/src/gen/vendor_pb.ts"
			const mock = fe + "/src/mocks/services/vendor-service_gen.ts"
			mustMkdir(t, filepath.Join(root, fe, "src/gen"))
			mustWrite(t, filepath.Join(root, stub), "/**\n * @generated from service vendor.v1.VendorService\n */\nexport const VendorService: GenService<{\n}> = serviceDesc(file_vendor, 0);\n")
			cs := &checksums.FileChecksums{}
			n, err := generator.EmitServiceDefaults(root, fe, cs)
			if err != nil || n != 1 {
				t.Fatalf("emit: n=%d err=%v", n, err)
			}
			before, err := os.ReadFile(filepath.Join(root, mock))
			if err != nil {
				t.Fatal(err)
			}
			if ownership == "edited" {
				before = append(before, []byte("\n// user edit\n")...)
				mustWrite(t, filepath.Join(root, mock), string(before))
			}
			if ownership == "disowned" {
				cs.Disowned = map[string]checksums.DisownedEntry{mock: {}}
			}
			resetCleanupRunState(t)
			// The helper overwrites the stub with text exporting no service, exactly
			// the scanner-visible effect of buf exclude_types.
			h := helperHook("vendor-refresh", "", "write", stub)
			h.Phase = "input"
			h.Writes = []string{stub}
			ctx := newCleanupCtx(root, cs, []codegen.ServiceDef{{Name: "KeptService"}}, &config.ProjectConfig{Generate: config.GenerateConfig{Hooks: []config.GenerateHook{h}}})
			// Execute the relevant production plan in its real order, including
			// both hook phases. The default phase must not rerun the input hook.
			for _, step := range generateSteps() {
				switch step.Name {
				case generateInputHooksStep, generateHooksStep:
					if step.Gate(ctx) {
						if err := step.Run(ctx); err != nil {
							t.Fatal(err)
						}
					}
				case "frontend mocks + transport":
					if _, err := generator.EmitServiceDefaults(root, fe, cs); err != nil {
						t.Fatal(err)
					}
				case "cleanup stale codegen":
					if err := step.Run(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := os.ReadFile(filepath.Join(root, mock))
			if ownership == "pristine" {
				if !os.IsNotExist(err) {
					t.Fatalf("stale mock remains: %v", err)
				}
			} else if err != nil || string(got) != string(before) {
				t.Fatalf("user-owned mock changed: %v", err)
			}
		})
	}
}
