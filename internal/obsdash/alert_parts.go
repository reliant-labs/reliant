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

// ReadAlertServices reads the alert inventory forge generate wrote under
// projectDir: one AlertService per alert-service part, sorted by file name.
// A project with no parts has none, which is not an error. It reads any
// project's tree, so an env that deploys another project's workloads can
// read that project's inventory too (ComposeAlertServices).
func ReadAlertServices(projectDir string) ([]AlertService, error) {
	return readAlertServices(projectDir)
}

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

// ComposeAlertServices is own plus every service of each composed inventory
// that own does not already watch, sorted by name (then rpc service, since
// two projects may each name a service "project").
//
// A service is the same service when its RPCService is: that is the only
// thing the defaults' SQL reads, and a project that mirrors another's proto
// (a client stub of reliant.v1.ProjectService) declares the same RPCService
// under its own name. The first declaration wins, own before the composed
// ones in order, so a service is watched once, under the deploying
// project's name when it declares one.
func ComposeAlertServices(own []AlertService, composed ...[]AlertService) []AlertService {
	seen := map[string]bool{}
	out := make([]AlertService, 0, len(own))
	for _, set := range append([][]AlertService{own}, composed...) {
		for _, s := range set {
			if seen[s.RPCService] {
				continue
			}
			seen[s.RPCService] = true
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].RPCService < out[j].RPCService
	})
	return out
}
