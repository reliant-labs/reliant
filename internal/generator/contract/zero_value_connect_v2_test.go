package contract

import "testing"

// A contract method returning a connect-go v2 interceptor, interceptor
// func or stream must zero to nil in the generated mock: they are func and
// interface types, where `T{}` does not compile. v1's interceptor
// interface is pinned beside them.
func TestZeroValue_ConnectV2FuncAndInterfaceTypes(t *testing.T) {
	for _, typ := range []string{
		"connect.Interceptor",
		"connect.ServerInterceptor", "connect.ClientInterceptor",
		"connect.ServerFunc", "connect.ClientFunc",
		"connect.ServerStream", "connect.ClientStream",
		"connectv2.ServerInterceptor", "connectv2.ClientInterceptor",
		"connectv2.ServerFunc", "connectv2.ClientFunc",
		"connectv2.ServerStream", "connectv2.ClientStream",
	} {
		if got := zeroValue(typ, nil, nil); got != "nil" {
			t.Errorf("zeroValue(%q) = %q, want nil", typ, got)
		}
	}
	// v2's Spec is a struct: it keeps its composite literal.
	if got := zeroValue("connect.Spec", nil, nil); got == "nil" {
		t.Errorf("zeroValue(connect.Spec) = nil, want a composite literal")
	}
}
