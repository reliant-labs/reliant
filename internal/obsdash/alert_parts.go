package obsdash

import (
	"os"
	"path/filepath"
	"sort"
)

// Alert inventory is not dashboard ownership: a service can be mounted by
// several processes. Keep each default's input once, separate from dashboards.
type alertServicePart struct {
	Version string       `json:"version"`
	Alert   AlertService `json:"alert"`
}

func alertServiceSpecFile(name string) string { return SpecDir + "/alert-service-" + name + genSuffix }

func readAlertServices(projectDir string) ([]AlertService, error) {
	matches, err := filepath.Glob(filepath.Join(projectDir, SpecDir, "alert-service-*"+genSuffix))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	out := []AlertService{}
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		var p alertServicePart
		if err := decodePart(SpecDir+"/"+filepath.Base(m), raw, &p, &p.Version); err != nil {
			return nil, err
		}
		out = append(out, p.Alert)
	}
	return out, nil
}
