// Copyright (c) 2025 Reliant Labs

//go:build !linux

package gate

import (
	"errors"
	"os"
)

var errSlotsNeedLinux = errors.New("disk slots need Linux")

func lockSlot(string) (*os.File, error) { return nil, errSlotsNeedLinux }

func slotSize(string, string) (int64, error) { return 0, errSlotsNeedLinux }
