// Copyright (c) 2026 Reliant Labs
//go:build !linux

package shell

import "os"

const punchAlign = 4096

// punchRead releases nothing outside Linux: an output file keeps all it was
// written until its process ends and the file is removed (fileOutput.finish).
func punchRead(_ *os.File, _, upto int64) int64 { return upto &^ (punchAlign - 1) }
