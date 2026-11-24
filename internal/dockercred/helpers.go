package dockercred

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// ConfigPath is the docker config file the docker CLI in this environment
// reads: $DOCKER_CONFIG/config.json, else ~/.docker/config.json. "" when no
// home directory is known.
func ConfigPath() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// MissingHelper is a credential helper the docker config names whose
// `docker-credential-<Helper>` executable is not on PATH. Every docker build
// or push that touches one of its Hosts fails with "error getting
// credentials ... executable file not found in $PATH".
type MissingHelper struct {
	Helper string
	// Hosts are the credHelpers hosts pinned to the helper; empty when it is
	// the config's credsStore (every host without a credHelpers entry).
	Hosts []string
}

// MissingHelpers lists the helpers configPath names (credHelpers values and
// credsStore) that lookPath cannot find as docker-credential-<helper>. A
// missing or unreadable config names none: docker then has no helper to run.
func MissingHelpers(configPath string, lookPath func(string) (string, error)) []MissingHelper {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return nil
	}
	var cfg struct {
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	hosts := map[string][]string{}
	for host, helper := range cfg.CredHelpers {
		if helper != "" {
			hosts[helper] = append(hosts[helper], host)
		}
	}
	if cfg.CredsStore != "" {
		if _, ok := hosts[cfg.CredsStore]; !ok {
			hosts[cfg.CredsStore] = nil
		}
	}
	var out []MissingHelper
	for helper, hs := range hosts {
		if _, err := lookPath("docker-credential-" + helper); err == nil {
			continue
		}
		sort.Strings(hs)
		out = append(out, MissingHelper{Helper: helper, Hosts: hs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Helper < out[j].Helper })
	return out
}

// HelperBesideTool finds docker-credential-<helper> in the real directory of
// a tool on PATH that ships it: the Cloud SDK installs docker-credential-gcloud
// beside gcloud, and a PATH that reaches gcloud through a symlink (~/go/bin/
// gcloud -> ~/google-cloud-sdk/bin/gcloud) does not reach the helper. It
// returns the directory holding the helper, or "".
func HelperBesideTool(helper string, lookPath func(string) (string, error)) string {
	tool, ok := map[string]string{"gcloud": "gcloud"}[helper]
	if !ok {
		return ""
	}
	p, err := lookPath(tool)
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(real)
	// exec.LookPath on a path judges it the way the OS runs it: the execute
	// bit on Unix, a PATHEXT extension (docker-credential-gcloud.cmd) on
	// Windows, where a mode bit means nothing.
	if _, err := exec.LookPath(filepath.Join(dir, "docker-credential-"+helper)); err != nil {
		return ""
	}
	return dir
}
