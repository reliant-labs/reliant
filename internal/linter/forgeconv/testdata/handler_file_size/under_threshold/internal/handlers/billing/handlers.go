// Small handler file fixture — under the test threshold. Counts as ~3
// source LOC after comment + blank stripping (the `package`, the
// `func`, and the function-body closing brace are merged conceptually
// but the counter sees three non-blank, non-comment lines).
//
// The lint should NOT fire on this file at threshold>=10.

package billing

func Stub() error {
	return nil
}
