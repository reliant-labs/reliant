package cluster

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/pkg/stsresize"
)

// validateResizeReleaseOwner is opt-in: old non-snapshot renders retain their
// behavior. An explicit owner must name exactly one manifest consumer on the
// actual generated cluster and declare the hook's exact runtime image slot.
func validateResizeReleaseOwner(owner, cluster, image string, raw any) error {
	if owner == "" {
		return nil
	}
	if strings.TrimSpace(owner) != owner {
		return fmt.Errorf("StatefulSetResize.release_owner must be a non-empty exact consumer name")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var consumers []struct {
		Name    string            `json:"name"`
		Cluster string            `json:"cluster"`
		Inputs  map[string]string `json:"release_inputs"`
	}
	if err := json.Unmarshal(data, &consumers); err != nil {
		return fmt.Errorf("decode resize release consumers: %w", err)
	}
	count := 0
	if image == "" {
		image = stsresize.DefaultImage
	}
	for _, c := range consumers {
		if c.Name != owner || c.Cluster != cluster {
			continue
		}
		count++
		declared := c.Inputs["container:resize"]
		if !sameResizeImage(declared, image) {
			return fmt.Errorf("StatefulSetResize.release_owner %q on cluster %q must declare container:resize matching the configured hook image", owner, cluster)
		}
	}
	if count != 1 {
		return fmt.Errorf("StatefulSetResize.release_owner %q must name exactly one explicit manifest consumer on cluster %q", owner, cluster)
	}
	return nil
}

func sameResizeImage(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ai, bi := strings.IndexByte(a, '@'), strings.IndexByte(b, '@')
	return ai >= 0 && bi >= 0 && a[ai+1:] == b[bi+1:] && hostedimage.Repository(a) == hostedimage.Repository(b)
}
