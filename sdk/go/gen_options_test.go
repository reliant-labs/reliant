// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package gosdk_test

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// The generated code does not link forge, so the (forge.v1.method) options on
// the public rpcs are not decoded here. They must still be in the descriptors,
// byte for byte, as unknown fields: a binary that does link forge (the app)
// decodes the same descriptors with them.
func TestForgeOptionsSurviveAsUnknownFields(t *testing.T) {
	md := reliantv1.File_reliant_v1_run_proto.Services().ByName("RunService").Methods().ByName("StartRun")
	if md == nil {
		t.Fatal("RunService.StartRun not found")
	}
	if len(md.Options().ProtoReflect().GetUnknown()) == 0 {
		t.Error("StartRun lost its forge.v1 method options: the descriptor must be unchanged")
	}
}
