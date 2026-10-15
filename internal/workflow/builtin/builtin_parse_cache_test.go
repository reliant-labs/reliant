// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// Every embedded builtin workflow, parsed and statically analysed ONCE per
// test binary.
//
// Parsing a builtin (runtime.ParseWorkflowProtoBytesWithLoader) is not a YAML
// decode: it validates every CEL expression, compiling each in a fresh CEL
// environment, and resolves builtin:// references through builtinLoader. Nine
// tests here each did that for every builtin, ~1.4s of CPU apiece, and two of
// them ran StaticAnalysis over all of them again. They run in parallel, so the
// repeats were most of the package's wall time. The assertions are unchanged:
// each test still checks every builtin, against the same parse.
type builtinParse struct {
	wf       *reliantv1.Workflow
	err      error
	analysis *validation.Result
}

var builtinParses = sync.OnceValue(func() map[string]*builtinParse {
	out := map[string]*builtinParse{}
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		// Every caller's own ReadDir reports this; parseBuiltin then says
		// the file is unknown.
		return out
	}
	var wg sync.WaitGroup
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		p := &builtinParse{}
		out[name] = p
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
			if err != nil {
				p.err = err
				return
			}
			p.wf, p.err = runtime.ParseWorkflowProtoBytesWithLoader(data, builtinLoader)
			if p.err == nil {
				p.analysis = validation.StaticAnalysis(p.wf, builtinLoader)
			}
		}()
	}
	wg.Wait()
	return out
})

// parseBuiltin returns what runtime.ParseWorkflowProtoBytesWithLoader returns
// for the embedded builtin filename, from the per-binary cache. The workflow
// is the caller's own clone, so a test may modify it.
func parseBuiltin(filename string) (*reliantv1.Workflow, error) {
	p, ok := builtinParses()[filename]
	if !ok {
		return nil, fmt.Errorf("no embedded builtin workflow %q", filename)
	}
	if p.err != nil {
		return nil, p.err
	}
	return proto.Clone(p.wf).(*reliantv1.Workflow), nil
}

// analyzeBuiltin returns validation.StaticAnalysis(wf, builtinLoader) for the
// unmodified embedded builtin filename, from the same cache. The Result is
// shared: read it, do not modify it. Only valid after parseBuiltin succeeded.
func analyzeBuiltin(filename string) *validation.Result {
	return builtinParses()[filename].analysis
}
