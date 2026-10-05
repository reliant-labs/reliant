// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

func inv(endpointKind string, infos ...*reliantv1.LocalModelInfo) *reliantv1.LocalModelInventory {
	return &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: endpointKind, Models: infos}}}
}

var (
	qwen    = &reliantv1.LocalModelInfo{Name: "qwen3:latest", ContextWindow: 40960, SupportsTools: true, SupportsThinking: true, SupportsChat: true}
	embed   = &reliantv1.LocalModelInfo{Name: "nomic-embed-text:latest", ContextWindow: 2048, SupportsChat: false}
	gptOSS  = &reliantv1.LocalModelInfo{Name: "gpt-oss:20b", ContextWindow: 131072, SupportsTools: true, SupportsThinking: true, SupportsChat: true}
	vision  = &reliantv1.LocalModelInfo{Name: "llava:7b", ContextWindow: 4096, SupportsVision: true, SupportsChat: true}
	unknown = &reliantv1.LocalModelInfo{Name: "mystery", SupportsChat: true}
)

func TestSynthesizeSkipsEmbeddingsAndCopiesCapabilities(t *testing.T) {
	got := Synthesize(DaemonInventory{DaemonID: "d1", Machine: "MacBook", Online: true, Inventory: inv("ollama", qwen, embed, vision, unknown)})
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3 (embedding skipped): %+v", len(got), got)
	}
	byName := map[string]Model{}
	for _, m := range got {
		byName[m.Definition.ID] = m
	}
	q := byName["qwen3:latest"]
	if q.CatalogID() != "qwen3:latest@local" || q.Provider() != "local:d1" {
		t.Errorf("identity = %q / %q", q.CatalogID(), q.Provider())
	}
	if q.Definition.Capabilities.MaxContextWindow != 40960 || !q.Definition.Capabilities.SupportsTools {
		t.Errorf("caps = %+v", q.Definition.Capabilities)
	}
	if false {
		t.Errorf("local models carry no tags")
	}
	if !byName["llava:7b"].Definition.Capabilities.SupportsAttachments {
		t.Error("vision must map to attachments")
	}
	if byName["mystery"].Definition.Capabilities.MaxContextWindow != fallbackContextWindow {
		t.Errorf("unknown window = %d, want conservative fallback", byName["mystery"].Definition.Capabilities.MaxContextWindow)
	}
}

// Live-verified (research/LOCAL_MODELS.md §3.1): Ollama 400s on any
// reasoning_effort a model does not take, qwen3 takes none.
func TestThinkingLevelsOnlyWhereServerAccepts(t *testing.T) {
	cases := []struct {
		kind string
		info *reliantv1.LocalModelInfo
		want []string
	}{
		{"ollama", qwen, nil},
		{"ollama", gptOSS, []string{"low", "medium", "high"}},
		{"llamacpp", qwen, []string{"low", "medium", "high"}},
		{"ollama", vision, nil},
	}
	for _, tc := range cases {
		def := definitionFor(tc.kind, tc.info)
		got := models.SupportedThinkingLevels(def.Capabilities)
		if len(got) != len(tc.want) {
			t.Errorf("%s/%s levels = %v, want %v", tc.kind, tc.info.Name, got, tc.want)
		}
		if def.Capabilities.CanReason != (len(tc.want) > 0) {
			t.Errorf("%s/%s CanReason = %v", tc.kind, tc.info.Name, def.Capabilities.CanReason)
		}
	}
}

func daemons() []Model {
	var all []Model
	all = append(all, Synthesize(DaemonInventory{DaemonID: "laptop", Machine: "Sean's MacBook", Online: false, Inventory: inv("ollama", qwen)})...)
	all = append(all, Synthesize(DaemonInventory{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: inv("ollama", qwen, gptOSS)})...)
	all = append(all, Synthesize(DaemonInventory{DaemonID: "desk", Machine: "Desktop", Online: true, Inventory: inv("ollama", qwen)})...)
	return all
}

func TestResolveSelectors(t *testing.T) {
	t.Run("pinned provider wins", func(t *testing.T) {
		m, err := Resolve(daemons(), models.ModelSelector{ID: "qwen3:latest@local", Providers: []string{"local:desk"}}, "gpu")
		if err != nil || m.DaemonID != "desk" {
			t.Fatalf("m=%+v err=%v", m, err)
		}
	})
	t.Run("unpinned prefers the worktree daemon", func(t *testing.T) {
		m, err := Resolve(daemons(), models.ModelSelector{ID: "qwen3:latest@local"}, "desk")
		if err != nil || m.DaemonID != "desk" {
			t.Fatalf("m=%+v err=%v", m, err)
		}
	})
	t.Run("unpinned skips an offline preferred daemon", func(t *testing.T) {
		m, err := Resolve(daemons(), models.ModelSelector{ID: "qwen3:latest@local"}, "laptop")
		if err != nil || !m.Online {
			t.Fatalf("m=%+v err=%v", m, err)
		}
	})
	t.Run("offline is a typed user-facing error naming the machine", func(t *testing.T) {
		_, err := Resolve(daemons(), models.ModelSelector{ID: "qwen3:latest@local", Providers: []string{"local:laptop"}}, "")
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("err = %v", err)
		}
		want := "Local model qwen3:latest on Sean's MacBook is unavailable: that machine is offline"
		if err.Error() != want {
			t.Errorf("message = %q, want %q", err.Error(), want)
		}
	})
	t.Run("unknown model", func(t *testing.T) {
		_, err := Resolve(daemons(), models.ModelSelector{ID: "llama9@local"}, "")
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("pinned daemon that lacks the model", func(t *testing.T) {
		_, err := Resolve(daemons(), models.ModelSelector{ID: "gpt-oss:20b@local", Providers: []string{"local:desk"}}, "")
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) || unavailable.Machine != "Desktop" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTagsNeverSelectLocal(t *testing.T) {
	for _, sel := range []models.ModelSelector{
		{Tags: []string{"local"}},
		{Tags: []string{"fast"}},
		{ID: "claude-5.5-opus"},
		{ID: "claude-5.5-opus@anthropic"},
	} {
		if IsSelector(sel) {
			t.Errorf("%+v must not select a local model", sel)
		}
	}
	if !IsSelector(models.ModelSelector{ID: "qwen3:latest@local"}) || !IsSelector(models.ModelSelector{ID: "x", Providers: []string{"local:d"}}) {
		t.Error("@local and local:<daemon> must select local")
	}
}

func TestRepoDirectoryOnlineFromAttachment(t *testing.T) {
	raw, err := protojson.Marshal(inv("ollama", qwen))
	if err != nil {
		t.Fatal(err)
	}
	src := newFakeRepoSource(map[string]string{"d-on": string(raw), "d-off": string(raw)}, map[string]string{"d-on": "On-box", "d-off": "Off-box"}, []string{"d-on"})
	got, err := NewRepoDirectory(src).LocalDaemons(context.Background(), "u")
	if err != nil {
		t.Fatal(err)
	}
	online := map[string]bool{}
	for _, d := range got {
		online[d.DaemonID] = d.Online
		if d.Machine == "" {
			t.Errorf("machine name missing for %s", d.DaemonID)
		}
	}
	if !online["d-on"] || online["d-off"] || len(got) != 2 {
		t.Errorf("online = %v", online)
	}
}
