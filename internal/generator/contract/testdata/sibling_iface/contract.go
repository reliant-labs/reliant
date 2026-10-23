package sibling_iface

// Service has a method returning a sibling-file interface (Handle).
// The mock generator must scan sibling .go files to discover Handle as
// an interface and emit "return nil" — not "return Handle{}".
type Service interface {
	NewHandle() Handle
}
