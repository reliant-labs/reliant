package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConfigureNodes migrates existing k3d nodes without deleting their volumes.
// --apply is explicit because restarting a single-server cluster interrupts it.
func (r Runner) ConfigureNodes(ctx context.Context, policyPath string, apply bool) error {
	if err := r.Local(ctx); err != nil {
		return err
	}
	file, err := NodeConfigPath(policyPath, r.Policy)
	if err != nil {
		return err
	}
	for _, cluster := range r.Policy.Clusters {
		if !strings.HasPrefix(cluster, "k3d-") {
			return fmt.Errorf("not a local k3d context: %s", cluster)
		}
		b, err := r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=15s", "get", "nodes", "-o", "json")
		if err != nil {
			return err
		}
		var nodes struct {
			Items []struct{ Metadata struct{ Name string } }
		}
		if err := json.Unmarshal(b, &nodes); err != nil {
			return err
		}
		for _, n := range nodes.Items {
			info, err := r.inspect(ctx, n.Metadata.Name)
			if err != nil {
				return err
			}
			// Inspect labels separately: no node merely named k3d-* is trusted.
			b, err = r.docker(ctx, "inspect", "--format", "{{json .Config.Labels}}", n.Metadata.Name)
			if err != nil {
				return err
			}
			var labels map[string]string
			if err := json.Unmarshal(b, &labels); err != nil {
				return err
			}
			if "k3d-"+labels["k3d.cluster"] != cluster || (labels["k3d.role"] != "server" && labels["k3d.role"] != "agent") || !info.State.Running {
				return fmt.Errorf("node %s does not belong to running local cluster %s", n.Metadata.Name, cluster)
			}
			b, err = r.docker(ctx, "exec", n.Metadata.Name, "k3s", "--version")
			if err != nil {
				return err
			}
			match := regexp.MustCompile(`k3s version v1\.([0-9]+)\.`).FindStringSubmatch(string(b))
			if len(match) != 2 {
				return fmt.Errorf("cannot establish k3s version: %s", b)
			}
			minor, _ := strconv.Atoi(match[1])
			if minor < 32 {
				return fmt.Errorf("%s requires k3s 1.32+ for kubelet drop-in configuration", n.Metadata.Name)
			}
			r.print("node %s: install unused-image age %s and restart (application volumes retained)\n", n.Metadata.Name, r.Policy.ImageUnused)
			if !apply {
				continue
			}
			const dir = "/var/lib/rancher/k3s/agent/etc/kubelet.conf.d"
			if _, err = r.docker(ctx, "exec", n.Metadata.Name, "mkdir", "-p", dir); err != nil {
				return err
			}
			mounted := false
			for _, m := range info.Mounts {
				if m.Destination == dir+"/90-forge-storage.conf" {
					mounted = true
				}
			}
			if !mounted {
				if _, err = r.docker(ctx, "cp", file, n.Metadata.Name+":"+dir+"/90-forge-storage.conf"); err != nil {
					return err
				}
			}
			if _, err = r.docker(ctx, "restart", n.Metadata.Name); err != nil {
				return err
			}
			deadline := time.Now().Add(3 * time.Minute)
			for {
				b, err = r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=10s", "get", "--raw", "/api/v1/nodes/"+n.Metadata.Name+"/proxy/configz")
				var actual struct {
					KubeletConfig struct{ ImageMaximumGCAge string }
				}
				if err == nil && json.Unmarshal(b, &actual) == nil && sameDuration(actual.KubeletConfig.ImageMaximumGCAge, r.Policy.ImageUnused) {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("node %s did not report configured imageMaximumGCAge; inspect kubelet configuration before continuing", n.Metadata.Name)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
		}
	}
	return nil
}

// LedgerPins imports historical local releases before registration enables GC.
// A missing ledger is normal; malformed entries abort rather than omit pins.
func LedgerPins(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pins []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var rel struct {
			Artifacts map[string]struct{ Digests map[string]string }
		}
		if err := json.Unmarshal(b, &rel); err != nil {
			return nil, err
		}
		for _, a := range rel.Artifacts {
			for _, d := range a.Digests {
				if !digestPattern.MatchString(d) {
					return nil, fmt.Errorf("invalid release digest in %s", e.Name())
				}
				pins = append(pins, d)
			}
		}
	}
	return pins, nil
}

func sameDuration(a, b string) bool {
	first, err := time.ParseDuration(a)
	if err != nil {
		return false
	}
	second, err := time.ParseDuration(b)
	return err == nil && first == second
}
