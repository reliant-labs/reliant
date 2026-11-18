package contractkit

import "fmt"

// MockNotSet returns the canonical "func field not set" error used by
// every Forge-generated mock when a method is invoked but the
// corresponding XxxFunc field has not been assigned by the test.
//
// The exact format string — "<MockName>.<Method>Func not set" — is part
// of the public surface. Tests in dogfood projects assert on this
// substring; the format is locked by TestMockNotSet_FingerprintLocked.
//
// Example (generated):
//
//	func (m *MockService) Send(ctx context.Context, args SendArgs) (SendResult, error) {
//	    m.Record("Send", ctx, args)
//	    if m.SendFunc != nil {
//	        return m.SendFunc(ctx, args)
//	    }
//	    return SendResult{}, contractkit.MockNotSet("MockService", "Send")
//	}
func MockNotSet(mockName, method string) error {
	return fmt.Errorf("%s.%sFunc not set", mockName, method)
}
