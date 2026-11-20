package settings

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListInstalledSkills was never implemented; it answers exactly as before.
func (s *Service) ListInstalledSkills(ctx context.Context, req *pb.ListInstalledSkillsRequest) (*pb.ListInstalledSkillsResponse, error) {
	return s.UnimplementedSettingsServiceHandler.ListInstalledSkills(ctx, req)
}
