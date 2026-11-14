package cliev

import "os"

// processEnv is Config.Getenv's default: the process environment.
//
// The variables a Client reads (DO_NOT_TRACK, <APP>_TELEMETRY,
// <APP>_TELEMETRY_QUIET and the CI markers in InCI) are a contract between the
// user and every CLI on the machine, not an app's configuration: a user who
// sets DO_NOT_TRACK=1 has opted out of every program at once, and no config
// file of the program's could say otherwise. pkg/.golangci.yml exempts exactly
// this file from the os.Getenv rule; a caller still passes Config.Getenv to
// decide for itself (tests do).
func processEnv(key string) string { return os.Getenv(key) }
