package obsquery

import (
	"strconv"
	"strings"
	"testing"
)

// PodUsage covers exactly the pods Pods lists, and reads one row past its
// cap so a cut is visible (review B1 of #2497): a listed pod is never left
// without usage because other pods' rows used the cap up unseen.
func TestPodUsageIsBoundToThePodsPodsLists(t *testing.T) {
	if MaxPodUsageRows < MaxWorkloadRows*len(UsageMetrics) {
		t.Fatalf("MaxPodUsageRows %d cannot hold every usage metric of one container per listed pod (%d)", MaxPodUsageRows, MaxWorkloadRows*len(UsageMetrics))
	}
	st, err := Workloads{Scope: scope(), Workload: "api"}.PodUsage()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ResourceAttributes['k8s.pod.uid']) IN (",
		"ORDER BY last_seen DESC, env, workload, pod_name LIMIT " + strconv.Itoa(MaxWorkloadRows),
		"LIMIT " + strconv.Itoa(MaxPodUsageRows+1),
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("PodUsage SQL lacks %q:\n%s", want, st.SQL)
		}
	}
}
