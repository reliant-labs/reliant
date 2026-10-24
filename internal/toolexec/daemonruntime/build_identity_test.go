// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/version"
)

func TestStampBuildIdentity(t *testing.T) {
	oldV, oldC, oldCT := version.Version, version.Commit, version.CommitTime
	t.Cleanup(func() { version.Version, version.Commit, version.CommitTime = oldV, oldC, oldCT })

	t.Run("a stamped build reports its identity", func(t *testing.T) {
		version.Version, version.Commit, version.CommitTime = "1.8.3-dev+b675d224", "b675d22417c3", "1792805271"
		reg := &reliantv1.DaemonRegister{}
		stampBuildIdentity(reg)
		assert.Equal(t, "1.8.3-dev+b675d224", reg.Version)
		assert.Equal(t, "b675d22417c3", reg.Commit)
		assert.Equal(t, int64(1792805271), reg.CommitTimeUnix)
		assert.Equal(t, knownOrEmpty(version.Forge()), reg.ForgeVersion)
	})

	t.Run("an unstamped build reports unknown as empty, never the sentinel", func(t *testing.T) {
		version.Version, version.Commit, version.CommitTime = "unknown", "unknown", "unknown"
		reg := &reliantv1.DaemonRegister{}
		stampBuildIdentity(reg)
		assert.Empty(t, reg.Version)
		assert.Empty(t, reg.Commit)
		assert.Zero(t, reg.CommitTimeUnix)
	})
}

// oldRegisterSchema is DaemonRegister as an older server compiled it: the same
// tags for the fields it knows, nothing at 13-16.
func oldRegisterSchema(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	boolean := descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("old_register.proto"),
		Package: proto.String("old"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("DaemonRegister"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("hostname"), Number: proto.Int32(3), Type: str, Label: opt},
				{Name: proto.String("daemon_type"), Number: proto.Int32(10), Type: str, Label: opt},
				{Name: proto.String("moves_on_drain"), Number: proto.Int32(12), Type: boolean, Label: opt},
			},
		}},
	}, nil)
	require.NoError(t, err)
	return fd.Messages().ByName("DaemonRegister")
}

// An older server must read a new daemon's registration without error and keep
// what it understands; the new tags are unknown fields to it.
func TestRegisterBuildFieldsAreIgnoredByOlderServers(t *testing.T) {
	wire, err := proto.Marshal(&reliantv1.DaemonRegister{
		Hostname: "h", DaemonType: "cloud", MovesOnDrain: true,
		Version: "1.8.3-dev+b675d224", Commit: "b675d22417c3", CommitTimeUnix: 1792805271, ForgeVersion: "v0.2.0",
	})
	require.NoError(t, err)

	old := dynamicpb.NewMessage(oldRegisterSchema(t))
	require.NoError(t, proto.Unmarshal(wire, old))
	fields := old.Descriptor().Fields()
	assert.Equal(t, "h", old.Get(fields.ByName("hostname")).String())
	assert.Equal(t, "cloud", old.Get(fields.ByName("daemon_type")).String())
	assert.True(t, old.Get(fields.ByName("moves_on_drain")).Bool())
	assert.NotEmpty(t, old.GetUnknown(), "tags 13-16 should survive as unknown fields, not fail the decode")
}

// A daemon that predates the fields sends none of them; a current server reads
// that as an unknown build.
func TestRegisterWithoutBuildFieldsReadsAsUnknownBuild(t *testing.T) {
	wire, err := proto.Marshal(&reliantv1.DaemonRegister{Hostname: "h", Platform: "linux", DaemonType: "local"})
	require.NoError(t, err)
	var got reliantv1.DaemonRegister
	require.NoError(t, proto.Unmarshal(wire, &got))
	assert.Empty(t, got.Version)
	assert.Empty(t, got.Commit)
	assert.Zero(t, got.CommitTimeUnix)
	assert.Empty(t, got.ForgeVersion)
}
