package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Exercise the actual worker constructor, not a manually attached consumer:
// losing the env mapping, Integrations conversion or registration must fail.
func TestTriggerDeliveryRelayPRRecordsFromEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, repositories string
		want               int
	}{
		{"default off", "", 0},
		{"excluded repository", "acme/other", 0},
		{"opted in", "acme/other,acme/app", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RELIANT_CONFIG", "")
			t.Setenv("RELIANT_PR_RECORD_REPOSITORIES", tc.repositories)
			root := &cobra.Command{Use: "reliant"}
			server := &cobra.Command{Use: "server"}
			worker := &cobra.Command{Use: "worker"}
			root.AddCommand(server)
			server.AddCommand(worker)
			config.RegisterFlags(root)
			config.RegisterServerProcessConfigFlags(server)
			// Only the integration block is needed; do not require live NATS
			// or Temporal configuration for an inbox projection test.
			cfg, err := config.LoadServerProcessConfigReach(worker, []string{"integrations"})
			require.NoError(t, err)
			settings := Integrations(cfg.GetIntegrations())
			if tc.repositories == "" {
				require.Empty(t, settings.PRRecordRepositories)
			} else {
				require.Equal(t, strings.Split(tc.repositories, ","), settings.PRRecordRepositories)
			}

			env := storetest.New(t)
			relay, err := NewTriggerDeliveryRelay(TriggerDeliveryRelayDeps{
				DB: env.Repo, Temporal: &TemporalClient{}, Integrations: &settings,
			})
			require.NoError(t, err)
			// A normalized, authenticated integration inbox receipt, as stored
			// by the webhook boundary. No triggers are needed for projection.
			payload, err := json.Marshal(struct{ Events []webhook.Event }{Events: []webhook.Event{{
				Type: "pull_request.opened", AccountKey: "inst-1", ResourceKey: "42",
				DeliveryID: "worker-config-test", Sender: &core.TriggerSender{},
				Attributes: map[string]string{"repository": "acme/app"},
				Data: map[string]any{"pull_request": map[string]any{
					"number": 7, "title": "wire projector", "state": "open",
					"updated_at": "2026-11-20T10:00:00Z",
					"head":       map[string]any{"ref": "feature", "sha": strings.Repeat("a", 40)},
					"base":       map[string]any{"ref": "main", "sha": strings.Repeat("b", 40)},
				}},
			}}})
			require.NoError(t, err)
			created, err := webhook.NewStores(env.Repo).RecordTriggerDelivery(t.Context(), core.TriggerDelivery{
				ID: uuid.NewString(), Source: core.TriggerDeliveryIntegration, Provider: "github",
				ScopeKey: "inst-1", DeliveryKey: "worker-config-test", ReceivedAt: time.Now().UTC(), Payload: payload,
			}, nil)
			require.NoError(t, err)
			require.True(t, created)
			worked, err := relay.relay.ProcessOne(t.Context())
			require.NoError(t, err)
			require.True(t, worked)
			var records, deliveries, failures int
			require.NoError(t, env.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pr_records WHERE repository = 'acme/app' AND number = 7").Scan(&records))
			require.NoError(t, env.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pr_record_deliveries").Scan(&deliveries))
			require.NoError(t, env.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM trigger_delivery_inbox WHERE failure_code = 'route_failed'").Scan(&failures))
			require.Equal(t, tc.want, records)
			require.Equal(t, tc.want, deliveries)
			require.Zero(t, failures)
		})
	}
}

// pipeline.enabled makes the worker's projector follow the repositories
// enrolled in the PR pipeline, with no RELIANT_PR_RECORD_REPOSITORIES: an
// enrolled repository's PR gets its record and its pr_pipeline row. Off, the
// same delivery records nothing.
func TestTriggerDeliveryRelayProjectsEnrolledRepositoriesWhenPipelineEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			env := storetest.New(t)
			repo, err := env.Repo.UpsertPipelineRepo(t.Context(), core.PipelineRepoEnrolment{
				OrgID: "org-1", ProjectID: storetest.SeedProjectID, Provider: "github", AccountKey: "inst-1",
				RepositoryID: "42", Repository: "acme/app", CreatedByUserID: storetest.SeedUserID,
			})
			require.NoError(t, err)
			_, err = env.Repo.SetPipelineRepoMode(t.Context(), repo.ID, repo.Version, core.PipelineRepoModeShadow)
			require.NoError(t, err)
			relay, err := NewTriggerDeliveryRelay(TriggerDeliveryRelayDeps{
				DB: env.Repo, Temporal: &TemporalClient{}, Integrations: &IntegrationSettings{},
				Pipeline: &configv1.PipelineConfig{Enabled: enabled},
			})
			require.NoError(t, err)
			payload, err := json.Marshal(struct{ Events []webhook.Event }{Events: []webhook.Event{{
				Type: "pull_request.opened", AccountKey: "inst-1", ResourceKey: "42",
				DeliveryID: "pipeline-wiring-test", Sender: &core.TriggerSender{},
				Attributes: map[string]string{"repository": "acme/app"},
				Data: map[string]any{"pull_request": map[string]any{
					"number": 7, "title": "wire projector", "state": "open", "merged": false,
					"updated_at": "2026-11-20T10:00:00Z",
					"head":       map[string]any{"ref": "feature", "sha": strings.Repeat("a", 40)},
					"base":       map[string]any{"ref": "main", "sha": strings.Repeat("b", 40)},
				}},
			}}})
			require.NoError(t, err)
			created, err := webhook.NewStores(env.Repo).RecordTriggerDelivery(t.Context(), core.TriggerDelivery{
				ID: uuid.NewString(), Source: core.TriggerDeliveryIntegration, Provider: "github",
				ScopeKey: "inst-1", DeliveryKey: "pipeline-wiring-test", ReceivedAt: time.Now().UTC(), Payload: payload,
			}, nil)
			require.NoError(t, err)
			require.True(t, created)
			worked, err := relay.relay.ProcessOne(t.Context())
			require.NoError(t, err)
			require.True(t, worked)
			var records, rows int
			require.NoError(t, env.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pr_records WHERE repository_id = '42' AND number = 7").Scan(&records))
			require.NoError(t, env.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pr_pipeline WHERE repo_id = $1", repo.ID).Scan(&rows))
			want := 0
			if enabled {
				want = 1
			}
			require.Equal(t, want, records)
			require.Equal(t, want, rows)
		})
	}
}
