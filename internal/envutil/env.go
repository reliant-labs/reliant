// Copyright (c) 2025 Reliant Labs
package envutil

import (
	"os"
	"strconv"
)

// GetEnv returns the value of the environment variable named by key,
// or defaultValue if the variable is empty or unset.
func GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetEnvInt returns the value of the environment variable named by key
// parsed as an integer, or defaultValue if the variable is empty, unset,
// or cannot be parsed.
func GetEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
