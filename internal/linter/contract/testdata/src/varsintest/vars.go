package varsintest

// On the package's API, so still flagged: the test-file skip is per file.
var GlobalConfig = "bad" // want `exported package variable GlobalConfig should be a method on a struct or a getter function`
