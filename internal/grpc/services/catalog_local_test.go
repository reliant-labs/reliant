// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
)

type fakeLocalCatalogDirectory struct{ daemons []local.DaemonInventory }

func (f fakeLocalCatalogDirectory) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return f.daemons, nil
}

func TestLocalModelInfosListsOnlineAndOfflineChatModels(t *testing.T) {
	qwen := &reliantv1.LocalModelInfo{Name: "qwen3:latest", ContextWindow: 40960, SupportsTools: true, SupportsThinking: true, SupportsChat: true}
	embed := &reliantv1.LocalModelInfo{Name: "nomic-embed-text:latest", ContextWindow: 2048}
	vision := &reliantv1.LocalModelInfo{Name: "llava:7b", ContextWindow: 4096, SupportsVision: true, SupportsChat: true}
	endpoint := func(models ...*reliantv1.LocalModelInfo) *reliantv1.LocalModelInventory {
		return &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: "ollama", Models: models}}}
	}
	svc := NewCatalogService(nil).WithLocalModels(fakeLocalCatalogDirectory{daemons: []local.DaemonInventory{
		{DaemonID: "d-on", Machine: "GPU box", Online: true, Inventory: endpoint(qwen, embed)},
		{DaemonID: "d-off", Machine: "Sean's MacBook", Online: false, Inventory: endpoint(qwen, vision)},
	}})

	got := svc.localModelInfos(context.Background(), "u1")
	require.Len(t, got, 3, "embedding model is not a chat model")

	byKey := map[string]*reliantv1.ModelInfo{}
	for _, info := range got {
		byKey[info.GetLocal().GetDaemonId()+"/"+info.GetId()] = info
	}

	on := byKey["d-on/qwen3:latest@local"]
	require.NotNil(t, on)
	assert.Equal(t, "local", on.GetDriverId())
	assert.Equal(t, int64(40960), on.GetContextWindow())
	assert.True(t, on.GetSupportsTools())
	assert.True(t, on.GetSupportsTemperature())
	assert.Empty(t, on.GetTags(), "local models carry no tags")
	assert.Equal(t, "GPU box", on.GetLocal().GetMachineName())
	assert.Equal(t, "ollama", on.GetLocal().GetEndpointId())
	assert.Equal(t, "ollama", on.GetLocal().GetEndpointKind())
	assert.True(t, on.GetLocal().GetOnline())

	off := byKey["d-off/qwen3:latest@local"]
	require.NotNil(t, off, "an offline daemon's model is listed so the UI can show it disabled")
	assert.False(t, off.GetLocal().GetOnline())

	assert.True(t, byKey["d-off/llava:7b@local"].GetSupportsAttachments())
}

func TestLocalModelInfosEmptyWithoutDirectory(t *testing.T) {
	assert.Empty(t, NewCatalogService(nil).localModelInfos(context.Background(), "u1"))
}
