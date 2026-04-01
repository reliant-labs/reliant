package wfyaml

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

var (
	protoMessageFullNameWorkflow      = (&reliantv1.Workflow{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameResponseTool  = (&reliantv1.ResponseTool{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameProjectConfig = (&reliantv1.ProjectConfig{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameStruct        = (&structpb.Struct{}).ProtoReflect().Descriptor().FullName()
)
