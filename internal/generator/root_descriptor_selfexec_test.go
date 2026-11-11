package generator_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/codegen"
)

// This test binary has no TestMain that serves `forge protoc-gen-forge`, so the
// root it builds must not register the descriptor extractor: the extractor
// runs os.Executable() (this binary) as the protoc plugin, which would rerun
// the whole suite, build the root again and recurse without bound. That hung
// forge main's unit 2/2 lane until the runner died (2026-11-10, after #2017).
func TestRootCmdInAPlainTestBinaryRegistersNoSelfExecDescriptorExtractor(t *testing.T) {
	_ = cli.NewRootCmd()
	if codegen.DescriptorBuilderRegistered() {
		t.Fatal("a test binary that cannot serve protoc-gen-forge registered the self-exec descriptor extractor")
	}
}
