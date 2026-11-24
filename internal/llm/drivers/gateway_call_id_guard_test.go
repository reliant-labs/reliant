// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only the reliant driver talks to Reliant's LLM gateway, so only it may
// record the gateway's call id (llm.RecordGatewayCallID) on a chat span. Any
// other driver that did would put an id on a call the gateway never billed,
// and the span-to-ledger join would match the wrong row. The guard reads every
// driver's source itself, so its cached result follows those files.
func TestOnlyTheGatewayDriverRecordsGatewayCallID(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Dir(path) == "reliant" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "RecordGatewayCallID") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("only internal/llm/drivers/reliant may record the gateway call id; found in %v", offenders)
	}
}
