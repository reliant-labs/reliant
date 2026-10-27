//go:build integration

package runtime

import "testing"

// PrivatePipelineHistoryPressure exposes only the existing per-chat test seam
// to the private integration harness. It is not compiled into production.
func PrivatePipelineHistoryPressure(t testing.TB, chatID string, soft func() bool) {
	t.Helper()
	if chatID == "" || soft == nil {
		t.Fatal("private pressure fixture requires unique chat and callback")
	}
	historyPressureOverrides.Store(chatID, func() historyPressure {
		if soft() {
			return historyPressureSoft
		}
		return historyPressureNone
	})
	t.Cleanup(func() { historyPressureOverrides.Delete(chatID) })
}
