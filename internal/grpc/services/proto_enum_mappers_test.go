package services

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/stretchr/testify/require"
)

func TestChatUpdateTypeFromString_MapsKnownValues(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected reliantv1.ChatUpdateType
	}{
		{
			name:     "message",
			input:    "message",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		},
		{
			name:     "streaming delta",
			input:    "streaming_delta",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA,
		},
		{
			name:     "message enum-style string from proto",
			input:    "CHAT_UPDATE_TYPE_MESSAGE",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		},
		{
			name:     "message enum-style string lowercase",
			input:    "chat_update_type_message",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		},
		{
			name:     "streaming delta enum-style string from proto",
			input:    "CHAT_UPDATE_TYPE_STREAMING_DELTA",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA,
		},
		{
			name:     "streaming delta enum-style string lowercase",
			input:    "chat_update_type_streaming_delta",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA,
		},
		{
			name:     "streaming delta with whitespace",
			input:    "  streaming_delta  ",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA,
		},
		{
			name:     "tool call",
			input:    "tool_call",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
		},
		{
			name:     "workflow status",
			input:    "workflow_status",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
		},
		{
			name:     "skill invocation",
			input:    "skill_invocation",
			expected: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_SKILL_INVOCATION,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			updateType := chatUpdateTypeFromString(testCase.input)
			require.Equal(t, testCase.expected, updateType)
		})
	}
}

func TestChatUpdateTypeFromString_UnknownDefaultsToUnspecified(t *testing.T) {
	updateType := chatUpdateTypeFromString("not-a-real-update")
	require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_UNSPECIFIED, updateType)
}

func TestPkgmgrPackageTypeFromProto_MapsKnownValues(t *testing.T) {
	pkgType, ok := pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_NPM)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeNPM, pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeMakefile, pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_TASKFILE)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeTaskfile, pkgType)
}

func TestPkgmgrPackageTypeFromProto_UnknownDefaultsToFalse(t *testing.T) {
	pkgType, ok := pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED)
	require.False(t, ok)
	require.Equal(t, pkgmgr.PackageType(""), pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType(99))
	require.False(t, ok)
	require.Equal(t, pkgmgr.PackageType(""), pkgType)
}
