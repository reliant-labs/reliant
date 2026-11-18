package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// PDBFinding is one PodDisruptionBudget that can never allow a voluntary
// disruption, so every node drain that touches its pods blocks forever.
type PDBFinding struct {
	Name      string
	Namespace string
	Reason    string
	Fix       string
}

func (f PDBFinding) String() string {
	return fmt.Sprintf("PodDisruptionBudget %q: %s — %s", f.Name, f.Reason, f.Fix)
}

type pdbDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Replicas       *int `yaml:"replicas"`
		MinAvailable   any  `yaml:"minAvailable"`
		MaxUnavailable any  `yaml:"maxUnavailable"`
		Selector       struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		} `yaml:"selector"`
		Template struct {
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// CheckPDBs reports every PodDisruptionBudget in a rendered manifest stream
// that can never permit a disruption: maxUnavailable 0, minAvailable 100%, or
// an integer minAvailable at or above the replica count of the Deployment /
// StatefulSet it selects. Replica-dependent checks need the selected workload
// in the same stream; a PDB selecting nothing we can see is judged only on the
// replica-independent rules.
func CheckPDBs(manifests string) []PDBFinding {
	var pdbs, workloads []pdbDoc
	for _, raw := range splitDocs(manifests) {
		var d pdbDoc
		if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
			continue
		}
		switch d.Kind {
		case "PodDisruptionBudget":
			pdbs = append(pdbs, d)
		case "Deployment", "StatefulSet":
			workloads = append(workloads, d)
		}
	}
	var out []PDBFinding
	for _, p := range pdbs {
		if f, bad := judgePDB(p, workloads); bad {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func judgePDB(p pdbDoc, workloads []pdbDoc) (PDBFinding, bool) {
	f := PDBFinding{Name: p.Metadata.Name, Namespace: p.Metadata.Namespace}
	if mu := p.Spec.MaxUnavailable; mu != nil {
		if v := strings.TrimSpace(fmt.Sprint(mu)); v == "0" || v == "0%" {
			f.Reason = "maxUnavailable is 0, so no pod may ever be evicted"
			f.Fix = "set maxUnavailable: 1 (or drop the PDB at one replica)"
			return f, true
		}
	}
	ma := p.Spec.MinAvailable
	if ma == nil {
		return f, false
	}
	v := strings.TrimSpace(fmt.Sprint(ma))
	if v == "100%" {
		f.Reason = "minAvailable is 100%, so no pod may ever be evicted"
		f.Fix = "set maxUnavailable: 1 instead"
		return f, true
	}
	min, err := strconv.Atoi(v)
	if err != nil {
		return f, false
	}
	for _, w := range workloads {
		if w.Metadata.Namespace != p.Metadata.Namespace || !labelsSubset(p.Spec.Selector.MatchLabels, w.Spec.Template.Metadata.Labels) {
			continue
		}
		replicas := 1
		if w.Spec.Replicas != nil {
			replicas = *w.Spec.Replicas
		}
		if min >= replicas {
			f.Reason = fmt.Sprintf("minAvailable %d >= %d replica(s) of %s %q, so no pod may ever be evicted", min, replicas, w.Kind, w.Metadata.Name)
			f.Fix = "set maxUnavailable: 1, or delete the PDB while the workload runs a single replica"
			return f, true
		}
	}
	return f, false
}

func labelsSubset(want, have map[string]string) bool {
	if len(want) == 0 {
		return false
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// WarnBlockingPDBs prints one warning per PDB CheckPDBs flags.
func WarnBlockingPDBs(manifests string) {
	for _, f := range CheckPDBs(manifests) {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", f)
	}
}

type livePDB struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

// stalePDBs picks, from the forge-managed PDBs currently in the namespace, the
// ones to delete: owned by a workload group the render still contains, but not
// themselves in the render. Workload groups absent from the render are left
// alone (a --target apply renders a subset, so absence proves nothing).
func stalePDBs(live []livePDB, manifests string) []string {
	groups := map[string]struct{}{}
	for _, g := range ManifestGroups(manifests) {
		groups[g] = struct{}{}
	}
	desired := map[string]struct{}{}
	for _, n := range renderedNamesByKind(manifests, "PodDisruptionBudget") {
		desired[n] = struct{}{}
	}
	var stale []string
	for _, p := range live {
		if p.Metadata.Labels["app.kubernetes.io/managed-by"] != "forge" {
			continue
		}
		if _, ok := groups[ManifestGroup(p.Metadata.Labels)]; !ok {
			continue
		}
		if _, keep := desired[p.Metadata.Name]; !keep {
			stale = append(stale, p.Metadata.Name)
		}
	}
	sort.Strings(stale)
	return stale
}

// PrunePDBs deletes forge-labelled PodDisruptionBudgets for workloads in this
// render whose PDB the render no longer carries (a workload dropping to one
// replica stops rendering its PDB, and apply never deletes). Only PDBs are
// touched, and only those labelled managed-by=forge.
func PrunePDBs(ctx context.Context, kctx, manifests, namespace string) error {
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	out, err := kubectlCmd(ctx, kctx, "get", "poddisruptionbudgets", "-n", namespace,
		"-l", "app.kubernetes.io/managed-by=forge", "-o", "json").Output()
	if err != nil {
		return fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	var list struct {
		Items []livePDB `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("parse poddisruptionbudgets: %w", err)
	}
	stale := stalePDBs(list.Items, manifests)
	if len(stale) == 0 {
		return nil
	}
	fmt.Printf("Pruning %d stale PodDisruptionBudget(s) in %s: %s\n", len(stale), namespace, strings.Join(stale, ", "))
	for _, name := range stale {
		del := kubectlCmd(ctx, kctx, "delete", "poddisruptionbudget", name, "-n", namespace, "--ignore-not-found=true")
		del.Stdout, del.Stderr = os.Stdout, os.Stderr
		if err := del.Run(); err != nil {
			fmt.Printf("  Warning: delete PodDisruptionBudget %s: %v\n", name, err)
		}
	}
	return nil
}
