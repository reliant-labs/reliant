package daemon_registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// cliLoginProbeTimeout bounds the status probe on the machine. Each vendor's
// check is bounded inside the script too (vendorcli.StatusScript); this is the
// whole run's ceiling.
const cliLoginProbeTimeout = 90 * time.Second

// CheckMachineCLILogins runs vendorcli.StatusScript on one of the caller's
// online machines and returns each vendor's verdict. The script comes from the
// vendor table, never the request: the request only picks which known
// vendors. It runs through exec.run, which every daemon has, so an old daemon
// answers too. What comes back is the verdict lines alone (the script sends
// every vendor command's own output to /dev/null), and only the parsed states
// leave this server.
func (s *Service) CheckMachineCLILogins(
	ctx context.Context,
	req *connect.Request[reliantv1.CheckMachineCLILoginsRequest],
) (*connect.Response[reliantv1.CheckMachineCLILoginsResponse], error) {
	vendors, err := cliLoginVendors(req.Msg.GetVendors())
	if err != nil {
		return nil, err
	}
	daemonID := req.Msg.GetDaemonId()
	userID, router, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(vendors))
	for _, v := range vendors {
		ids = append(ids, v.ID)
	}
	payload, err := json.Marshal(daemon.RunCommandRequest{
		Command:   vendorcli.StatusScript(ids),
		TimeoutMs: int(cliLoginProbeTimeout / time.Millisecond),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}
	raw, err := router.SendDaemonCommandToDaemon(ctx, userID, daemonID, "exec.run", payload,
		int32((cliLoginProbeTimeout+10*time.Second)/time.Millisecond))
	if err != nil {
		return nil, localModelRelayError("checking the machine's agent CLI sign-ins", err)
	}
	var result daemon.CommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("decode exec.run response"))
	}
	details := vendorcli.ParseStatusDetail(result.Stdout)
	resp := &reliantv1.CheckMachineCLILoginsResponse{}
	for _, v := range vendors {
		d := details[v.ID]
		resp.Logins = append(resp.Logins, &reliantv1.MachineCLILogin{
			Vendor: v.ID, Name: v.Name, State: CLILoginStateProto(d.State), OffPath: d.OffPath,
		})
	}
	return connect.NewResponse(resp), nil
}

// cliLoginVendors is the vendors a request names, in the table's order; none
// is all of them. An id the table does not know is InvalidArgument, naming the
// ones it does.
func cliLoginVendors(requested []string) ([]vendorcli.Vendor, error) {
	if len(requested) == 0 {
		return vendorcli.All(), nil
	}
	want := map[string]bool{}
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if _, ok := vendorcli.Lookup(id); !ok {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("unknown vendor CLI %q: want one of %s", id, strings.Join(vendorcli.IDs(), ", ")))
		}
		want[id] = true
	}
	var out []vendorcli.Vendor
	for _, v := range vendorcli.All() {
		if want[v.ID] {
			out = append(out, v)
		}
	}
	return out, nil
}

// CLILoginStateProto maps a vendorcli verdict onto the wire enum; a missing
// or unrecognized verdict is UNKNOWN.
func CLILoginStateProto(s vendorcli.State) reliantv1.CLILoginState {
	switch s {
	case vendorcli.StateSignedIn:
		return reliantv1.CLILoginState_CLI_LOGIN_STATE_SIGNED_IN
	case vendorcli.StateSignedOut:
		return reliantv1.CLILoginState_CLI_LOGIN_STATE_SIGNED_OUT
	case vendorcli.StateExpired:
		return reliantv1.CLILoginState_CLI_LOGIN_STATE_EXPIRED
	case vendorcli.StateMissing:
		return reliantv1.CLILoginState_CLI_LOGIN_STATE_NOT_INSTALLED
	default:
		return reliantv1.CLILoginState_CLI_LOGIN_STATE_UNKNOWN
	}
}
