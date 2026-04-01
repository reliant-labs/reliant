// Copyright (c) 2025 Reliant Labs

package osutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

func generateProcessSignature(command string, startTime time.Time) string {
	data := fmt.Sprintf("%s|%d", command, startTime.UnixNano())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for shorter signature
}
