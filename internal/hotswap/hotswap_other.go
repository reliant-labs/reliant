// Copyright (c) 2026 Reliant Labs

//go:build !linux

package hotswap

import "os"

// Supported reports whether this platform can hand commands across an exec.
const Supported = false

func stateCarrier([]byte) (*os.File, error)   { return nil, ErrUnsupported }
func readState(*os.File) ([]byte, error)      { return nil, ErrUnsupported }
func setInheritable([]*os.File, bool) error   { return ErrUnsupported }
func execve(string, []string, []string) error { return ErrUnsupported }
func adopt(fd int, name string) *os.File      { return os.NewFile(uintptr(fd), name) }
