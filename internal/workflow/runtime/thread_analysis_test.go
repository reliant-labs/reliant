// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeThreadMapping(t *testing.T) {
	t.Parallel()
	t.Run("basic operations", func(t *testing.T) {
		m := NewRuntimeThreadMapping()

		// Record a thread resolution
		m.RecordThreadResolution("thread:step1:0", "uuid-123")
		assert.Equal(t, "uuid-123", m.GetActualUUID("thread:step1:0"))
		assert.Equal(t, LogicalThreadName("thread:step1:0"), m.GetLogicalName("uuid-123"))

		// Mark step completed
		assert.False(t, m.IsStepCompleted("step1"))
		m.MarkStepCompleted("step1")
		assert.True(t, m.IsStepCompleted("step1"))
	})

	t.Run("nil safety", func(t *testing.T) {
		var m *RuntimeThreadMapping

		// Should not panic
		m.RecordThreadResolution("thread:x", "uuid")
		m.MarkStepCompleted("x")
		assert.Equal(t, "", m.GetActualUUID("thread:x"))
		assert.Equal(t, LogicalThreadName(""), m.GetLogicalName("uuid"))
		assert.False(t, m.IsStepCompleted("x"))
	})
}

func TestThreadTracker(t *testing.T) {
	t.Parallel()
	t.Run("basic operations", func(t *testing.T) {
		tracker := NewThreadTracker()

		// Record thread resolution
		tracker.Mapping.RecordThreadResolution(ThreadRoot, "root-uuid")
		tracker.Mapping.RecordThreadResolution("thread:child:0", "child-uuid")

		// Get statuses
		statuses := tracker.GetAllThreadStatuses()
		assert.Len(t, statuses, 2)

		// Check that all are active (threads are always active while workflow running)
		for _, status := range statuses {
			assert.True(t, status.IsActive)
		}
	})

	t.Run("get single thread status", func(t *testing.T) {
		tracker := NewThreadTracker()
		tracker.Mapping.RecordThreadResolution(ThreadRoot, "root-uuid")

		status := tracker.GetThreadStatus(ThreadRoot)
		require.NotNil(t, status)
		assert.Equal(t, ThreadRoot, status.LogicalName)
		assert.Equal(t, "root-uuid", status.ActualUUID)
		assert.True(t, status.IsActive)
	})

	t.Run("nil safety", func(t *testing.T) {
		var tracker *ThreadTracker

		assert.Nil(t, tracker.GetAllThreadStatuses())
		assert.Nil(t, tracker.GetThreadStatus(ThreadRoot))
	})

	t.Run("step completion tracking", func(t *testing.T) {
		tracker := NewThreadTracker()

		// Mark steps completed
		assert.False(t, tracker.Mapping.IsStepCompleted("step1"))
		tracker.Mapping.MarkStepCompleted("step1")
		assert.True(t, tracker.Mapping.IsStepCompleted("step1"))
	})
}

func TestLogicalThreadName(t *testing.T) {
	t.Parallel()
	t.Run("ThreadRoot constant", func(t *testing.T) {
		assert.Equal(t, "thread:root", string(ThreadRoot))
	})

	t.Run("String method", func(t *testing.T) {
		name := LogicalThreadName("thread:test:0")
		assert.Equal(t, "thread:test:0", name.String())
	})
}
