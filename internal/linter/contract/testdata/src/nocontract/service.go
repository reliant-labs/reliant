package nocontract

// No contract.go in this package — linter should skip entirely.

type MyType struct{}

func (m *MyType) DoAnything() string {
	return "anything"
}

func ExportedFunc() {}
