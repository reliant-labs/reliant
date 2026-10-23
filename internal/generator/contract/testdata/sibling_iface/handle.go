package sibling_iface

// Handle is declared in a sibling file (not contract.go). The contract
// generator must still recognize it as an interface so the mock fallback
// emits "nil" instead of the invalid composite literal "Handle{}".
type Handle interface {
	Close() error
}
