package vulnpatch

import (
	"os"
	"path/filepath"
	"strings"
)

// pythonVerify is a Python project's own test run at dir, when one is
// natural: pytest, when the project configures it (pytest.ini,
// [tool.pytest.ini_options], [pytest] in tox.ini, [tool:pytest] in
// setup.cfg) or declares it as a dependency, run through the lockfile's own
// tool, which runs it in the environment the bump changed: `uv run pytest
// -q` beside uv.lock (uv syncs first), `poetry run pytest -q` beside
// poetry.lock (`poetry add` and `poetry update` installed the bump).
//
// A project pinned only by requirements files has none: which environment
// the bumped requirements install into is the project's choice, so
// vuln.patch.verify or --verify=<cmd> names it. ("", "") when there is none.
func pythonVerify(dir string) (command, source string) {
	var tool Ecosystem
	switch {
	case exists(filepath.Join(dir, "uv.lock")):
		tool = EcosystemUV
	case exists(filepath.Join(dir, "poetry.lock")):
		tool = EcosystemPoetry
	default:
		return "", ""
	}
	if !usesPytest(dir) {
		return "", ""
	}
	return string(tool) + " run pytest -q", string(tool)
}

// usesPytest reports a project at dir that configures pytest or declares it.
func usesPytest(dir string) bool {
	if exists(filepath.Join(dir, "pytest.ini")) {
		return true
	}
	for file, section := range map[string]string{"pyproject.toml": "[tool.pytest.ini_options]", "tox.ini": "[pytest]", "setup.cfg": "[tool:pytest]"} {
		if data, err := os.ReadFile(filepath.Join(dir, file)); err == nil && hasSection(string(data), section) {
			return true
		}
	}
	decls, err := readPyProject(filepath.Join(dir, "pyproject.toml"))
	return err == nil && len(decls["pytest"]) > 0
}

func hasSection(ini, header string) bool {
	for _, line := range strings.Split(ini, "\n") {
		if strings.TrimSpace(line) == header {
			return true
		}
	}
	return false
}
