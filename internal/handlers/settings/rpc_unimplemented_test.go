package settings_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

// The rpcs in rpc_unimplemented.go were never implemented. Each answers
// exactly as the embedded Unimplemented handler always did, not with forge's
// scaffold stub.
func TestNeverImplementedRPCsAnswerUnimplemented(t *testing.T) {
	t.Parallel()

	svc := settings.NewTestSettings(t, settings.WithPlaceholderDeps())
	ctx := settings.AuthedContext(t)

	for name, call := range map[string]func(context.Context) error{
		"TrackOnboardingEvent": func(ctx context.Context) error {
			_, err := svc.TrackOnboardingEvent(ctx, connect.NewRequest(&pb.TrackOnboardingEventRequest{}))
			return err
		},
		"InstallSkill": func(ctx context.Context) error {
			_, err := svc.InstallSkill(ctx, connect.NewRequest(&pb.InstallSkillRequest{}))
			return err
		},
		"ListInstalledSkills": func(ctx context.Context) error {
			_, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&pb.ListInstalledSkillsRequest{}))
			return err
		},
		"GetInstalledSkillDefinition": func(ctx context.Context) error {
			_, err := svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&pb.GetInstalledSkillDefinitionRequest{}))
			return err
		},
		"SetSkillEnabled": func(ctx context.Context) error {
			_, err := svc.SetSkillEnabled(ctx, connect.NewRequest(&pb.SetSkillEnabledRequest{}))
			return err
		},
		"ListRecommendedSkills": func(ctx context.Context) error {
			_, err := svc.ListRecommendedSkills(ctx, connect.NewRequest(&pb.ListRecommendedSkillsRequest{}))
			return err
		},
		"DeleteGlobalSkill": func(ctx context.Context) error {
			_, err := svc.DeleteGlobalSkill(ctx, connect.NewRequest(&pb.DeleteGlobalSkillRequest{}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call(ctx)
			require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
			require.ErrorContains(t, err, "reliant.v1.SettingsService."+name+" is not implemented")
		})
	}
}
