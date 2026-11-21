package settings

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SetSkillEnabled was never implemented; it answers exactly as before.
func (s *Service) SetSkillEnabled(ctx context.Context, req *connect.Request[pb.SetSkillEnabledRequest]) (*connect.Response[pb.SetSkillEnabledResponse], error) {
	return s.UnimplementedSettingsServiceHandler.SetSkillEnabled(ctx, req)
}
