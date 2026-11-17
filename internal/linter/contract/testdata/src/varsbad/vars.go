package varsbad

import "errors"

// Sentinel errors are OK.
var ErrSomething = errors.New("something")

// These should be flagged.
var GlobalConfig = "bad" // want `exported package variable GlobalConfig should be a method on a struct or a getter function`
var DefaultTimeout = 30  // want `exported package variable DefaultTimeout should be a method on a struct or a getter function`
var MaxRetries = 3       // want `exported package variable MaxRetries should be a method on a struct or a getter function`
