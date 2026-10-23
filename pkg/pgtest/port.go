package pgtest

import "net"

// ReserveLoopbackPort binds an ephemeral loopback TCP port, reads the
// number the OS assigned, and releases it.
//
// Exported because every harness that has to hand a concrete port to a
// process it is about to spawn needs exactly this: embedded-postgres here,
// and the service binary a generated e2e harness starts. Without it each
// harness re-implements the same bind-:0/read/close trick.
//
// The port is FREE at the moment of return, not reserved — the caller must
// bind it promptly and tolerate the (small) race where another process
// claims it in between. There is no portable way to close that race; an
// OS-assigned ephemeral port is still far better than a hardcoded one.
func ReserveLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
