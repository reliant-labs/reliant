package wfyaml

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// CELExprSentinelKey carries a CEL expression through a google.protobuf.Struct
// field, which ResolveCELFields walks for VALUES but cannot evaluate for SHAPE.
//
// A Struct's schema is the data, so `schema: "{{inputs.x}}"` and
// `tools: "{{inputs.y}}"` have nowhere typed to live until the expression is
// resolved. Wrapping the expression as the sole key of a one-entry Struct gives
// it a home the resolver already visits; post-resolution unwrapping replaces
// the whole Struct with what the expression produced.
//
// Exported so the runtime that unwraps it and the parser that writes it name
// the same string. They are in different packages, and a silent typo here
// degrades to "the template was ignored", which is the failure this key exists
// to make impossible.
const CELExprSentinelKey = "__cel_expr__"

var (
	protoMessageFullNameWorkflow      = (&reliantv1.Workflow{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameResponseTool  = (&reliantv1.ResponseTool{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameProjectConfig = (&reliantv1.ProjectConfig{}).ProtoReflect().Descriptor().FullName()
	protoMessageFullNameStruct        = (&structpb.Struct{}).ProtoReflect().Descriptor().FullName()
)
