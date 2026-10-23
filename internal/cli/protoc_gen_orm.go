package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

// forgePluginMode controls what the protoc-gen-forge plugin generates.
//
// Only descriptor mode exists: the plugin extracts services/configs
// metadata into forge_descriptor.json fragments. The historical
// mode=orm (entity-proto → *.pb.orm.go codegen) is gone — entities are
// projections of the applied db/migrations schema now, not of proto
// annotations.
type forgePluginMode string

const (
	forgePluginModeDescriptor forgePluginMode = "descriptor"
)

// validateForgePluginMode maps the plugin's mode option to a
// forgePluginMode. mode=orm gets a dedicated removal message so legacy
// buf templates fail loudly with the migration path instead of a
// generic "unknown mode".
func validateForgePluginMode(value string) (forgePluginMode, error) {
	switch forgePluginMode(value) {
	case forgePluginModeDescriptor:
		return forgePluginModeDescriptor, nil
	case "orm":
		return "", fmt.Errorf("protoc-gen-forge mode=orm was removed: entities are projected from the applied db/migrations schema (run `forge generate`); delete the buf template that requests mode=orm")
	default:
		return "", fmt.Errorf("unknown mode: %s (valid: descriptor)", value)
	}
}

func newProtocGenForgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "protoc-gen-forge",
		Short:  "Protoc plugin for descriptor extraction (invoked by buf)",
		Hidden: true,
		// A buf subprocess, not a user command: never touch git config,
		// and never print a nudge meant for a person — buf spawns this
		// once per proto file.
		Annotations: map[string]string{
			skipHookActivationAnnotation: "",
			machineInvokedAnnotation:     "",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// protogen.Options{}.Run() reads os.Args directly and rejects
			// any arg that doesn't start with "--". When invoked as
			// `forge protoc-gen-forge` by buf, os.Args[1] is the
			// subcommand name. Strip it so protogen sees only the binary.
			os.Args = os.Args[:1]

			mode := forgePluginModeDescriptor
			descriptorOut := "gen" // default output directory

			opts := protogen.Options{
				ParamFunc: func(name, value string) error {
					switch name {
					case "mode":
						m, err := validateForgePluginMode(value)
						if err != nil {
							return err
						}
						mode = m
					case "descriptor_out":
						descriptorOut = value
					}
					return nil
				},
			}

			opts.Run(func(p *protogen.Plugin) error {
				p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
				_ = mode // single-mode plugin; kept for the ParamFunc contract
				return generateDescriptor(p, descriptorOut)
			})
			return nil
		},
	}
}
