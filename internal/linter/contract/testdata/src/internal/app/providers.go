package app

// Infra is the owned provider set (scaffold-once in real projects).
type Infra struct {
	timeout int
}

// DefaultClient mirrors the scaffold-once providers.go accessor — an
// exported method on a struct in a package with no contract.go. The
// composition-seam exemption keeps the require-contract rule quiet here.
func (i *Infra) DefaultClient() int {
	return i.timeout
}
