package settings

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// InstallSkill was never implemented; it answers exactly as before.
func (s *Service) InstallSkill(ctx context.Context, req *pb.InstallSkillRequest) (*pb.InstallSkillResponse, error) {
	return s.UnimplementedSettingsServiceHandler.InstallSkill(ctx, req)
}
