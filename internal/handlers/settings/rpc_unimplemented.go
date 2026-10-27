package settings

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// These rpcs were never implemented. Each answers exactly as before, through
// the embedded Unimplemented handler rather than forge's scaffold stub.

// TrackOnboardingEvent was never implemented (rpc_privacy_settings.go keeps
// the draft commented out); it answers exactly as before.
func (s *Service) TrackOnboardingEvent(ctx context.Context, req *connect.Request[pb.TrackOnboardingEventRequest]) (*connect.Response[pb.TrackOnboardingEventResponse], error) {
	return s.UnimplementedSettingsServiceHandler.TrackOnboardingEvent(ctx, req)
}

// InstallSkill was never implemented; it answers exactly as before.
func (s *Service) InstallSkill(ctx context.Context, req *connect.Request[pb.InstallSkillRequest]) (*connect.Response[pb.InstallSkillResponse], error) {
	return s.UnimplementedSettingsServiceHandler.InstallSkill(ctx, req)
}

// ListInstalledSkills was never implemented; it answers exactly as before.
func (s *Service) ListInstalledSkills(ctx context.Context, req *connect.Request[pb.ListInstalledSkillsRequest]) (*connect.Response[pb.ListInstalledSkillsResponse], error) {
	return s.UnimplementedSettingsServiceHandler.ListInstalledSkills(ctx, req)
}

// GetInstalledSkillDefinition was never implemented; it answers exactly as
// before.
func (s *Service) GetInstalledSkillDefinition(ctx context.Context, req *connect.Request[pb.GetInstalledSkillDefinitionRequest]) (*connect.Response[pb.GetInstalledSkillDefinitionResponse], error) {
	return s.UnimplementedSettingsServiceHandler.GetInstalledSkillDefinition(ctx, req)
}

// SetSkillEnabled was never implemented; it answers exactly as before.
func (s *Service) SetSkillEnabled(ctx context.Context, req *connect.Request[pb.SetSkillEnabledRequest]) (*connect.Response[pb.SetSkillEnabledResponse], error) {
	return s.UnimplementedSettingsServiceHandler.SetSkillEnabled(ctx, req)
}

// ListRecommendedSkills was never implemented; it answers exactly as before.
func (s *Service) ListRecommendedSkills(ctx context.Context, req *connect.Request[pb.ListRecommendedSkillsRequest]) (*connect.Response[pb.ListRecommendedSkillsResponse], error) {
	return s.UnimplementedSettingsServiceHandler.ListRecommendedSkills(ctx, req)
}

// DeleteGlobalSkill was never implemented; it answers exactly as before.
func (s *Service) DeleteGlobalSkill(ctx context.Context, req *connect.Request[pb.DeleteGlobalSkillRequest]) (*connect.Response[pb.DeleteGlobalSkillResponse], error) {
	return s.UnimplementedSettingsServiceHandler.DeleteGlobalSkill(ctx, req)
}
