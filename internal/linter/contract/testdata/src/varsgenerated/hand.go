package varsgenerated

// HandRolled proves the generated-file exemption is per-FILE: a
// hand-written var in the same package as exempt generated files is
// still flagged.
var HandRolled = "still flagged" // want `exported package variable HandRolled should be a method on a struct or a getter function`
