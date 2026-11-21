// File: env.go — the one place pkg/credentials reads the process environment.
//
// Where the credentials file lives is a published contract between every CLI
// that shares it: $FORGE_HOME, then $XDG_CONFIG_HOME, then the home directory
// (the package doc's "WHERE IT LIVES"). FORGE_HOME is forge's own variable and
// XDG_CONFIG_HOME is the OS convention; neither is an app's configuration, so
// no typed config object can carry them. Every CLI used to read the two
// variables itself and fill Dirs by hand — the same three lines in `forge
// login` and in each consumer, which is how two tools end up disagreeing about
// where one file is. DirsFromEnv is those three lines, once.
//
// pkg/.golangci.yml exempts exactly this file from the os.Getenv rule.

package credentials

import "os"

// Environment variable names of the contract. Exported so docs and doctor
// checks refer to one spelling.
const (
	// EnvForgeHome redirects the file to $FORGE_HOME/credentials.json. It wins
	// over everything else, so a test or a sandboxed CI job can move the file
	// without touching a developer's real credentials.
	EnvForgeHome = "FORGE_HOME"
	// EnvXDGConfigHome places the file at $XDG_CONFIG_HOME/forge/credentials.json.
	EnvXDGConfigHome = "XDG_CONFIG_HOME"
)

// DirsFromEnv returns the Dirs this process's environment names: $FORGE_HOME,
// $XDG_CONFIG_HOME and the user's home directory. It is how `forge login`
// locates the file, so a consumer that calls it shares the file with forge by
// construction.
//
// An unknown home directory leaves Dirs.Home empty; Path then fails only when
// neither variable is set either, which is the one case with nowhere to put
// the file.
func DirsFromEnv() Dirs {
	home, _ := os.UserHomeDir()
	return Dirs{
		ForgeHome:     os.Getenv(EnvForgeHome),
		XDGConfigHome: os.Getenv(EnvXDGConfigHome),
		Home:          home,
	}
}

// PathFromEnv is DirsFromEnv().Path(): where the credentials file lives for
// this process.
func PathFromEnv() (string, error) {
	return DirsFromEnv().Path()
}
