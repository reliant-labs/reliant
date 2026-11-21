//go:build integration

package observe

import "time"

// The headers-file smoke (headers_file_smoke_integration_test.go) renders the
// node agent through pkg/deploy, and pkg/deploy imports this package, so the
// smoke is an external test (package observe_test): an internal one is an
// import cycle and never compiled. These hand it the internal test helpers it
// shares with the unit tests.
var (
	ClearOTelEnvForTest     = clearOTelEnv
	SnapshotGlobalsForTest  = snapshotGlobals
	WriteHeadersFileForTest = writeHeadersFile
)

// HeadersFileRecheckForTest is how often the exporter re-reads the headers
// file: the smoke exports for longer than that to prove a token is refused.
func HeadersFileRecheckForTest() time.Duration { return headersFileRecheck }
