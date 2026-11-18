package testfake

// BindingStore is the package's contract. store_test.go declares a fake that
// satisfies it, the way reliant's internal/mcpserver/http_test.go does.
type BindingStore interface {
	Get(key string) (string, bool)
	Put(key, value string)
}
