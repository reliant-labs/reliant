package settings

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DeleteGlobalSkill was never implemented; it answers exactly as before.
func (s *Service) DeleteGlobalSkill(ctx context.Context, req *connect.Request[pb.DeleteGlobalSkillRequest]) (*connect.Response[pb.DeleteGlobalSkillResponse], error) {
	return s.UnimplementedSettingsServiceHandler.DeleteGlobalSkill(ctx, req)
}
