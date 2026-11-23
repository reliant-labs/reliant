// Command configcheck is the boot config check's fixture binary: a forge-config
// process shaped like control-plane's delivery-signer, built by
// config_check_slow_test.go and run in the check mode. Its config is built at
// run time (no generated code), with the two invariants the signer broke in
// prod on 2026-11-03: a required secret (INTERNAL_SERVICE_SECRET) and a rule
// that the signer needs a registry base (IMAGE_BUILD_REGISTRY_BASE).
//
// `configcheck delivery_signer` loads it and, outside the check, would run;
// here it prints "started" so a test can tell a check that never stopped the
// process from one that did.
package main

import (
	"fmt"
	"os"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"
	"github.com/reliant-labs/forge/pkg/forgepb"
)

func main() {
	msg := signerConfig()
	root := &cobra.Command{Use: "configcheck"}
	signer := &cobra.Command{
		Use: "delivery_signer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := msg.New().Interface()
			if err := forgeconfig.Load(cmd, cfg); err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			fmt.Println("started")
			return nil
		},
	}
	if err := forgeconfig.RegisterFlags(signer, msg.New().Interface()); err != nil {
		panic(err)
	}
	root.AddCommand(signer)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func signerConfig() *dynamicpb.Message {
	field := func(name string, n int32, typ descriptorpb.FieldDescriptorProto_Type, o *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, o)
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: typ.Enum(), Options: fo,
		}
	}
	mo := &descriptorpb.MessageOptions{}
	proto.SetExtension(mo, validate.E_Message, &validate.MessageRules{Cel: []*validate.Rule{{
		Id:         proto.String("signer_needs_registry_base"),
		Message:    proto.String("delivery_signing.signer_enabled needs image_build.registry_base"),
		Expression: proto.String("!this.signer_enabled || this.registry_base != ''"),
	}}})
	str, boolean := descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_BOOL
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("configcheck_fixture.proto"),
		Package: proto.String("configcheck.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("AppConfig"),
			Options: mo,
			Field: []*descriptorpb.FieldDescriptorProto{
				field("internal_service_secret", 1, str, &forgepb.ConfigFieldOptions{EnvVar: "INTERNAL_SERVICE_SECRET", Sensitive: true, Required: true}),
				field("signer_enabled", 2, boolean, &forgepb.ConfigFieldOptions{EnvVar: "DELIVERY_SIGNER_ENABLED"}),
				field("registry_base", 3, str, &forgepb.ConfigFieldOptions{EnvVar: "IMAGE_BUILD_REGISTRY_BASE"}),
			},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		panic(err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("AppConfig"))
}
