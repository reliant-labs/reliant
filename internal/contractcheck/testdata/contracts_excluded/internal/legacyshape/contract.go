// Same non-canonical shape as contracts_bad, but this directory is
// excluded via contracts.exclude in forge.yaml. Lint must produce
// zero findings even though the contract violates the convention.
package legacyshape

type Manager interface {
	Install(name string) error
}
