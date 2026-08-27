package configloader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
)

func TestLoadMCPServersFromProjectScopes_Precedence(t *testing.T) {
	userConfigDir := t.TempDir()
	projectDir := t.TempDir()

	t.Setenv("RELIANT_USER_CONFIG_DIR", userConfigDir)

	globalPath := filepath.Join(userConfigDir, config.MCPConfigFileName)
	projectPath := filepath.Join(projectDir, config.ReliantDir, config.MCPConfigFileName)
	projectLocalPath := filepath.Join(projectDir, config.ReliantLocalDir, config.MCPConfigFileName)

	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(projectLocalPath), 0o755); err != nil {
		t.Fatalf("mkdir project-local dir: %v", err)
	}

	globalCfg := `{
  "mcpServers": {
    "shared": {
      "command": "global-cmd",
      "args": ["a"],
      "env": {"K":"global"},
      "type": "stdio"
    },
    "global-only": {
      "command": "g-only",
      "type": "stdio"
    }
  }
}`
	projectCfg := `{
  "mcpServers": {
    "shared": {
      "command": "project-cmd",
      "args": ["b"],
      "env": {"K":"project"},
      "type": "stdio"
    },
    "project-only": {
      "command": "p-only",
      "type": "stdio"
    }
  }
}`
	projectLocalCfg := `{
  "mcpServers": {
    "shared": {
      "command": "local-cmd",
      "args": ["c"],
      "env": {"K":"local"},
      "type": "stdio"
    },
    "local-only": {
      "command": "l-only",
      "type": "stdio"
    }
  }
}`

	if err := os.WriteFile(globalPath, []byte(globalCfg), 0o644); err != nil {
		t.Fatalf("write global mcp: %v", err)
	}
	if err := os.WriteFile(projectPath, []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project mcp: %v", err)
	}
	if err := os.WriteFile(projectLocalPath, []byte(projectLocalCfg), 0o644); err != nil {
		t.Fatalf("write project-local mcp: %v", err)
	}

	servers := LoadMCPServersFromProjectScopes(projectDir)
	if len(servers) != 4 {
		t.Fatalf("expected 4 servers, got %d", len(servers))
	}

	shared := servers["shared"]
	if shared.Command != "local-cmd" {
		t.Fatalf("expected shared command from project-local scope, got %q", shared.Command)
	}

	if servers["global-only"].Command != "g-only" {
		t.Fatalf("expected global-only server to be present")
	}
	if servers["project-only"].Command != "p-only" {
		t.Fatalf("expected project-only server to be present")
	}
	if servers["local-only"].Command != "l-only" {
		t.Fatalf("expected local-only server to be present")
	}
}

func TestLoadMCPServersFromFile_ExpandsArgsFromServerEnv(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, config.MCPConfigFileName)

	cfg := `{
  "mcpServers": {
    "statsig": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "https://api.statsig.com/v1/mcp",
        "--header",
        "statsig-api-key:${AUTH_TOKEN}"
      ],
      "env": {
        "AUTH_TOKEN": "console-test-token"
      },
      "type": "stdio"
    }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	servers := LoadMCPServersFromFile(mcpPath)
	s, ok := servers["statsig"]
	if !ok {
		t.Fatalf("expected statsig server")
	}
	if len(s.Args) < 5 {
		t.Fatalf("expected args to be parsed, got %#v", s.Args)
	}
	if got := s.Args[4]; got != "statsig-api-key:console-test-token" {
		t.Fatalf("expected header arg expansion from server env, got %q", got)
	}
}

func TestLoadMCPServersFromFile_ParsesTypeAndURL(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, config.MCPConfigFileName)

	cfg := `{
  "mcpServers": {
    "remote": {
      "command": "",
      "type": "sse",
      "url": "https://example.com/mcp"
    }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	servers := LoadMCPServersFromFile(mcpPath)
	s, ok := servers["remote"]
	if !ok {
		t.Fatalf("expected remote server")
	}
	if s.Type != config.MCPSse {
		t.Fatalf("expected type sse, got %q", s.Type)
	}
	if s.URL != "https://example.com/mcp" {
		t.Fatalf("expected url to be parsed, got %q", s.URL)
	}
}

// mcp.json is parsed field by hand, so a field added to config.MCPServer is
// silently dropped until it is added here. Both dirScoped and dir had been
// declared and documented for users while never being read out of the file,
// which made the whole feature inert from a config file; requiresFiles would
// have inherited exactly that.
func TestLoadMCPServersFromFile_ParsesTreeScopingFields(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, config.MCPConfigFileName)

	cfg := `{
  "mcpServers": {
    "gopls": {
      "command": "gopls",
      "args": ["mcp"],
      "dirScoped": true,
      "requiresFiles": ["go.mod", "go.work", "  "]
    },
    "pinned": {
      "command": "gopls",
      "args": ["mcp"],
      "dir": "/srv/monorepo"
    },
    "plain": {
      "command": "some-tool"
    }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	servers := LoadMCPServersFromFile(mcpPath)

	gopls, ok := servers["gopls"]
	if !ok {
		t.Fatalf("expected gopls server")
	}
	if !gopls.DirScoped {
		t.Error("dirScoped was dropped: every project would share one index")
	}
	if len(gopls.RequiresFiles) != 2 || gopls.RequiresFiles[0] != "go.mod" || gopls.RequiresFiles[1] != "go.work" {
		t.Errorf("requiresFiles not parsed (blanks dropped): %#v", gopls.RequiresFiles)
	}

	if got := servers["pinned"].Dir; got != "/srv/monorepo" {
		t.Errorf("dir was dropped: got %q", got)
	}

	plain := servers["plain"]
	if plain.DirScoped || len(plain.RequiresFiles) != 0 || plain.Dir != "" {
		t.Errorf("a server declaring none of these must get none of them: %#v", plain)
	}
}
