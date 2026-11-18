// A package under internal/workers/ that is NOT a worker, whose test file
// declares a fake that presents the worker lifecycle (Name/Start). The fake
// is not the package's surface, so it must not buy the package the worker
// exemption. Finding expected.
package fakeworkerintest // want "package fakeworkerintest has exported methods but no contract.go"

type Helper struct{}

func (h *Helper) Compute() error { return nil }
