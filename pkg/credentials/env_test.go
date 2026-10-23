package credentials

import (
	"path/filepath"
	"testing"
)

// DirsFromEnv is how `forge login` locates the file, so every precedence step
// Dirs.Path documents must be reachable from the environment alone.
func TestDirsFromEnv_Precedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

	t.Setenv(EnvForgeHome, "/fh")
	t.Setenv(EnvXDGConfigHome, "/xdg")
	if got, want := DirsFromEnv(), (Dirs{ForgeHome: "/fh", XDGConfigHome: "/xdg", Home: home}); got != want {
		t.Fatalf("DirsFromEnv() = %+v, want %+v", got, want)
	}
	assertPathFromEnv(t, "FORGE_HOME set", filepath.FromSlash("/fh/credentials.json"))

	t.Setenv(EnvForgeHome, "")
	assertPathFromEnv(t, "only XDG_CONFIG_HOME set", filepath.FromSlash("/xdg/forge/credentials.json"))

	t.Setenv(EnvXDGConfigHome, "")
	assertPathFromEnv(t, "neither set", filepath.Join(home, ".config", "forge", FileName))
}

func assertPathFromEnv(t *testing.T, env, want string) {
	t.Helper()
	got, err := PathFromEnv()
	if err != nil || got != want {
		t.Fatalf("%s: PathFromEnv() = %q, %v; want %q", env, got, err, want)
	}
}
