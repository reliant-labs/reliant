// This file fixture is intentionally heavy on comments and blank lines
// to verify the LOC counter strips both before measuring size. The total
// raw line count is well past 30, but actual source LOC is tiny.
//
// The lint should NOT fire on this file at threshold=10.

/*
 * Block comment that runs across multiple lines without contributing
 * to source LOC.
 *
 * Even more padding.
 *
 * And more.
 */

// Single-line comment block #1.
// Single-line comment block #2.
// Single-line comment block #3.
// Single-line comment block #4.
// Single-line comment block #5.

package billing

// Stub does nothing.
//
// It is the only non-comment, non-blank function body in this file.
func Stub() error {
	return nil
}

// Trailing comment to push raw line count higher without adding LOC.
// Trailing comment line 2.
// Trailing comment line 3.
// Trailing comment line 4.
// Trailing comment line 5.
