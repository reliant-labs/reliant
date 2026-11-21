package settings

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// InstallSkill was never implemented; it answers exactly as before.
func (s *Service) InstallSkill(ctx context.Context, req *connect.Request[pb.InstallSkillRequest]) (*connect.Response[pb.InstallSkillResponse], error) {
	return s.UnimplementedSettingsServiceHandler.InstallSkill(ctx, req)
}
