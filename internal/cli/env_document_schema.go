package cli

// THE ENV SCHEMA: the public, versioned contract of an env's declaration.
//
// An env's declaration is the document `output = forge.render(bundle)`
// evaluates to. That document is an API in its own right: a hand-written env
// document (deploy/envs/<env>.yaml) IS one, and every forge command reads an
// env through it. Its JSON Schema is GENERATED from the Go types that decode
// it — envDocumentWire below, which is the entity parser's kclRenderRaw plus
// the keys internal/cluster decodes itself — so the schema cannot describe a
// field the decoder does not read, or miss one it does.
//
// The schema is committed at schema/env.v1.schema.json and embedded in the
// binary (`forge project schema env` prints it). TestEnvSchemaIsCurrent
// regenerates it and fails on drift; `FORGE_UPDATE_GOLDEN=1 go test
// ./internal/cli -run TestEnvSchemaIsCurrent` rewrites it after a field is
// added. docs/design/env-document.md records the design.

import (
	"encoding/json"
	"reflect"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/schemagen"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	forgeschema "github.com/reliant-labs/forge/schema"
)

// envDocumentAPIVersion names the env schema and its major version. A
// document carries it as `apiVersion`; a change that would reject a document
// v1 accepted is a new major (v2), never an edit of v1.
const envDocumentAPIVersion = "forge.dev/env/v1"

// envSchemaID is where the schema of each release is published: the file at
// the release's tag. `main` is the newest; pin a tag in `$schema` to validate
// against the forge you run.
const envSchemaID = "https://raw.githubusercontent.com/reliant-labs/forge/main/schema/env.v1.schema.json"

// envDocumentWire is the root of the env schema: one env's declaration, as
// written by hand or rendered by KCL.
//
// It is the entity parser's wire type (kclRenderRaw, embedded so its fields
// are this object's) plus the keys only internal/cluster's manifest
// extraction reads, which shadow kclRenderRaw's loose views of the same keys
// (encoding/json: the shallower field wins).
type envDocumentWire struct {
	// APIVersion names the schema the document conforms to:
	// "forge.dev/env/v1". Required in a hand-written env document; a KCL
	// render omits it (the forge that rendered it is the version).
	APIVersion string `json:"apiVersion,omitempty"`
	// Schema is the editor hint `$schema`: the URL of this schema. Ignored
	// by forge.
	Schema string `json:"$schema,omitempty"`

	kclRenderRaw

	// NetworkPolicy is the env-wide NetworkPolicy bundle's inputs, or null
	// when the env declares no forge.NetworkPolicy.
	NetworkPolicy *cluster.NetworkPolicyOutput `json:"network_policy,omitempty"`
	// StatefulSetResize tunes the StatefulSet volume-resize Job; present only
	// when declared.
	StatefulSetResize *cluster.StatefulSetResizeOutput `json:"statefulset_resize,omitempty"`
	// Overrides are Bundle.overrides: per-object patches ("Kind/name" to a
	// patch map) applied to the rendered stream.
	Overrides map[string]map[string]any `json:"overrides,omitempty"`
	// BundledCharts are the HelmCharts delivered in the bundle (delivery =
	// "bundle"), expanded into the apply stream.
	BundledCharts []cluster.BundledChartOutput `json:"bundled_charts,omitempty"`
	// Generated are the forge.Generated commands whose YAML joins the apply
	// stream.
	Generated []cluster.GeneratedOutput `json:"generated,omitempty"`
	// Manifests is the applyable stream: Kubernetes objects, including one
	// forge.dev/v1alpha1 Workload record per cluster-bound workload.
	Manifests []map[string]any `json:"manifests,omitempty"`
	// ConfigMaps are Bundle.config_maps as declared. Informational: the
	// objects themselves are in manifests.
	ConfigMaps []map[string]any `json:"config_maps,omitempty"`
	// RuntimeClasses are Bundle.runtime_classes as declared. Informational:
	// the objects themselves are in manifests.
	RuntimeClasses []map[string]any `json:"runtime_classes,omitempty"`
}

// envSchemaOptions declares what reflection cannot see: which properties are
// required, and the fields the parser decodes through json.RawMessage into a
// tagged union (dispatchRuntime, dispatchBuild, decodeWorkload).
func envSchemaOptions(describe func(reflect.Type, string) string) schemagen.Options {
	tWorkload := reflect.TypeOf(kclWorkloadRaw{})
	return schemagen.Options{
		ID:    envSchemaID,
		Title: "forge env document (" + envDocumentAPIVersion + ")",
		Description: "One forge environment's declaration: the document a KCL env (deploy/kcl/<env>/main.k, " +
			"`output = forge.render(bundle)`) renders to, or a hand-written env document (deploy/envs/<env>.yaml). " +
			"Generated from the Go types forge decodes it with. See docs/design/env-document.md.",
		Required: map[reflect.Type][]string{
			reflect.TypeOf(envDocumentWire{}): {"project", "env"},
			tWorkload:                         {"name", "runtime"},
		},
		Types: map[reflect.Type]func(*schemagen.Generator) (*schemagen.Schema, error){
			// The frontend runtime union (FrontendRuntime.UnmarshalJSON): a
			// bucket or firebase runtime carries its block, the rest only
			// their type.
			reflect.TypeOf(FrontendRuntime{}): func(g *schemagen.Generator) (*schemagen.Schema, error) {
				return schemagen.TaggedUnion(g, "type", envFrontendRuntimeVariants)
			},
			// A protojson 64-bit integer: a number, or a string holding one.
			reflect.TypeOf(wireInt64(0)): func(*schemagen.Generator) (*schemagen.Schema, error) {
				return &schemagen.Schema{AnyOf: []*schemagen.Schema{{Types: []string{"integer"}}, {Types: []string{"string"}}}}, nil
			},
			// A path / BuildKit reference, or a forge.GitSource
			// (BuildContext.UnmarshalJSON).
			reflect.TypeOf(config.BuildContext{}): func(g *schemagen.Generator) (*schemagen.Schema, error) {
				src, err := g.Ref(reflect.TypeOf(config.GitSource{}))
				if err != nil {
					return nil, err
				}
				return &schemagen.Schema{AnyOf: []*schemagen.Schema{{Types: []string{"string"}}, src}}, nil
			},
			// `context` and `dockerfile` are each a path or a forge.GitSource
			// (DockerBuild.UnmarshalJSON, decodePathOrSource).
			reflect.TypeOf(DockerBuild{}): func(g *schemagen.Generator) (*schemagen.Schema, error) {
				s, err := g.Struct(reflect.TypeOf(DockerBuild{}))
				if err != nil {
					return nil, err
				}
				src, err := g.Ref(reflect.TypeOf(config.GitSource{}))
				if err != nil {
					return nil, err
				}
				for _, f := range []string{"context", "dockerfile"} {
					s.SetProperty(f, schemagen.Nullable(&schemagen.Schema{AnyOf: []*schemagen.Schema{{Types: []string{"string"}}, src}}))
				}
				return s, nil
			},
		},
		Fields: map[schemagen.FieldKey]func(*schemagen.Generator) (*schemagen.Schema, error){
			{Type: tWorkload, Name: "runtime"}: func(g *schemagen.Generator) (*schemagen.Schema, error) {
				return schemagen.TaggedUnion(g, "type", envRuntimeVariants)
			},
			{Type: tWorkload, Name: "build"}: func(g *schemagen.Generator) (*schemagen.Schema, error) {
				str := &schemagen.Schema{Types: []string{"string", "null"}}
				return schemagen.TaggedUnion(g, "type", envBuildVariants,
					schemagen.Property{Name: "unit", Schema: str},
					schemagen.Property{Name: "inputs_file", Schema: str})
			},
			{Type: tWorkload, Name: "spec"}: func(g *schemagen.Generator) (*schemagen.Schema, error) {
				return g.Ref(reflect.TypeOf(deployv1alpha1.WorkloadSpec{}))
			},
		},
		Describe: describe,
	}
}

// envRuntimeVariants is the runtime union dispatchRuntime decodes:
// `runtime.type` to the type its block decodes into.
// TestEnvSchemaUnionsMatchDispatch holds the two together.
var envRuntimeVariants = map[string]reflect.Type{
	RuntimeHost:      reflect.TypeOf(HostRuntime{}),
	RuntimeCompose:   reflect.TypeOf(ComposeRuntime{}),
	RuntimeCluster:   reflect.TypeOf(ClusterRuntime{}),
	RuntimeHosted:    reflect.TypeOf(HostedRuntime{}),
	RuntimeBuildOnly: reflect.TypeOf(BuildOnlyDeploy{}),
	RuntimeExternal:  reflect.TypeOf(deploytarget.ExternalRuntime{}),
}

// envFrontendRuntimeVariants is the union FrontendRuntime.UnmarshalJSON
// decodes; nil is a runtime with no block of its own.
var envFrontendRuntimeVariants = map[string]reflect.Type{
	FrontendRuntimeHost:      nil,
	FrontendRuntimeHosted:    nil,
	FrontendRuntimeBuildOnly: nil,
	FrontendRuntimeBucket:    reflect.TypeOf(BucketRuntime{}),
	FrontendRuntimeFirebase:  reflect.TypeOf(FirebaseRuntime{}),
}

// envBuildVariants is the build union dispatchBuild decodes.
var envBuildVariants = map[string]reflect.Type{
	"go":     reflect.TypeOf(GoBuild{}),
	"docker": reflect.TypeOf(DockerBuild{}),
	"shell":  reflect.TypeOf(ShellBuild{}),
	"remote": reflect.TypeOf(RemoteBuild{}),
}

// generateEnvSchema derives the env schema from the Go types. describe
// supplies doc comments (nil: none); the committed schema is generated with
// them, from source, by the drift test.
func generateEnvSchema(describe func(reflect.Type, string) string) ([]byte, error) {
	s, err := schemagen.Generate(reflect.TypeOf(envDocumentWire{}), envSchemaOptions(describe))
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// envSchemaJSON is the published env schema, as embedded in this binary.
func envSchemaJSON() []byte { return forgeschema.EnvV1() }
