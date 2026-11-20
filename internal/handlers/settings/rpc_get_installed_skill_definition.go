package settings

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetInstalledSkillDefinition was never implemented; it answers exactly as
// before.
func (s *Service) GetInstalledSkillDefinition(ctx context.Context, req *pb.GetInstalledSkillDefinitionRequest) (*pb.GetInstalledSkillDefinitionResponse, error) {
	return s.UnimplementedSettingsServiceHandler.GetInstalledSkillDefinition(ctx, req)
}
