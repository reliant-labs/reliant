// Copyright (c) 2025 Reliant Labs
package integration

import (
	"time"

	"github.com/stretchr/testify/suite"
)

// Test timing constants - optimized for faster test execution
const (
	// FastTimeout is for quick operations that should complete in seconds
	FastTimeout  = 10 * time.Second
	FastInterval = 100 * time.Millisecond

	// MediumTimeout is for workflow operations that may take longer
	MediumTimeout  = 30 * time.Second
	MediumInterval = 250 * time.Millisecond

	// SlowTimeout is for complex e2e scenarios with multiple steps
	SlowTimeout  = 60 * time.Second
	SlowInterval = 500 * time.Millisecond
)

// EventuallyWithTimeout wraps suite.Eventually with explicit timeout and interval
func EventuallyWithTimeout(s *suite.Suite, condition func() bool, timeout time.Duration, interval time.Duration, msgAndArgs ...interface{}) bool {
	return s.Eventually(condition, timeout, interval, msgAndArgs...)
}

// EventuallyFast wraps suite.Eventually with fast timeout/interval for quick checks
func EventuallyFast(s *suite.Suite, condition func() bool, msgAndArgs ...interface{}) bool {
	return s.Eventually(condition, FastTimeout, FastInterval, msgAndArgs...)
}

// EventuallyMedium wraps suite.Eventually with medium timeout/interval for workflow operations
func EventuallyMedium(s *suite.Suite, condition func() bool, msgAndArgs ...interface{}) bool {
	return s.Eventually(condition, MediumTimeout, MediumInterval, msgAndArgs...)
}

// EventuallySlow wraps suite.Eventually with slow timeout/interval for complex e2e tests
func EventuallySlow(s *suite.Suite, condition func() bool, msgAndArgs ...interface{}) bool {
	return s.Eventually(condition, SlowTimeout, SlowInterval, msgAndArgs...)
}
