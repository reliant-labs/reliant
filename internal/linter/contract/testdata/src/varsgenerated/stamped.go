// forge:hash=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
package varsgenerated

// StampedInventory exercises the forge:hash fallback: the file carries
// forge's self-certification marker but NOT the canonical stdlib-form
// "Code generated" banner, so ast.IsGenerated alone would miss it.
// Still generated → still exempt → NO finding expected.
var StampedInventory = []string{"a", "b"}
