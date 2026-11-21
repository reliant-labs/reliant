package settings

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListRecommendedSkills was never implemented; it answers exactly as before.
func (s *Service) ListRecommendedSkills(ctx context.Context, req *connect.Request[pb.ListRecommendedSkillsRequest]) (*connect.Response[pb.ListRecommendedSkillsResponse], error) {
	return s.UnimplementedSettingsServiceHandler.ListRecommendedSkills(ctx, req)
}
