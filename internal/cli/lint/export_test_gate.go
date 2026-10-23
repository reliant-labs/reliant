package lint

// WriteGateForTest exposes the installed gate writer so the CLI package can
// assert that --gate-json is actually wired. A test-only seam in production
// code, deliberately: the wiring happens in an init() in the CLI package, and
// nothing else can observe whether it ran.
func WriteGateForTest(req GateRequest) error {
	if writeGate == nil {
		return errGateWriterNotInstalled
	}
	return writeGate(req)
}
