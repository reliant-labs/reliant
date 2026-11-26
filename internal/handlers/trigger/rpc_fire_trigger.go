package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// The bounds FireTriggerRequest's buf.validate rules declare. The API chain
// does not enforce protovalidate, so the handler holds them itself.
const (
	maxFireInputs         = 64
	maxFireInputNameLen   = 128
	maxFireInputsBytes    = 64 << 10 // as PreviewReplay's sample event
	maxIdempotencyKeyLen  = 255
	idempotencyDedupeMark = ":idem:"
)

// FireTrigger runs a trigger now. The firing is asynchronous: this returns the
// fire workflow id, and the outcome shows up in ListTriggerEvents.
//
// An event trigger also takes the workflow inputs for this one firing and an
// idempotency key; see FireTriggerRequest.
func (s *Service) FireTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.FireTriggerRequest],
) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	supplied, err := fireInputsFrom(req.Msg)
	if err != nil {
		return nil, err
	}
	key, err := idempotencyKeyFrom(req.Msg)
	if err != nil {
		return nil, err
	}
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	eventDriven := trigger.Kind.IsEventDriven()
	if !eventDriven && (len(supplied) > 0 || key != "") {
		return nil, svcerr.InvalidArgument("inputs and idempotency_key: only an event trigger (webhook, integration or workflow event) takes them")
	}
	// The caller's inputs are judged first, so a bad one is the caller's
	// InvalidArgument rather than the activation's "needs setup".
	if eventDriven && len(supplied) > 0 {
		if err := s.checkFireInputs(ctx, trigger, supplied); err != nil {
			return nil, err
		}
	}
	// Run now launches with the activation's params, so one that is not set up
	// would start a run that fails its first input check. Say so up front. An
	// input this request supplies counts toward what the activation needs.
	setup := trigger
	if eventDriven {
		setup = triggers.WithFireParams(trigger, supplied)
	}
	if err := s.requireSetupComplete(ctx, setup); err != nil {
		return nil, err
	}
	if eventDriven {
		fired, err := s.fireInbound(ctx, trigger, supplied, key)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(fired), nil
	}
	if s.deps.Fires == nil {
		return nil, svcerr.Unavailable("the schedule backend is unavailable")
	}

	fireID, err := s.deps.Fires.StartManualFire(ctx, trigger.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("start fire failed"))
	}
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
}

// fireInputsFrom is the request's inputs as plain values, refused when they
// break the bounds FireTriggerRequest.inputs declares.
func fireInputsFrom(msg *reliantv1.FireTriggerRequest) (map[string]any, error) {
	inputs := msg.GetInputs()
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > maxFireInputs {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("inputs: at most %d inputs", maxFireInputs))
	}
	size := 0
	out := make(map[string]any, len(inputs))
	for name, value := range inputs {
		if name == "" || len(name) > maxFireInputNameLen {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("inputs: an input name is 1 to %d characters", maxFireInputNameLen))
		}
		if value == nil {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("inputs.%s: a value is required", name))
		}
		size += len(name) + proto.Size(value)
		out[name] = value.AsInterface()
	}
	if size > maxFireInputsBytes {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("inputs: at most %d KiB", maxFireInputsBytes>>10))
	}
	return out, nil
}

// idempotencyKeyFrom is the request's idempotency key: "" when it carries
// none, an error when it is set but not 1 to 255 printable ASCII characters.
func idempotencyKeyFrom(msg *reliantv1.FireTriggerRequest) (string, error) {
	if msg.IdempotencyKey == nil {
		return "", nil
	}
	key := msg.GetIdempotencyKey()
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return "", svcerr.InvalidArgument(fmt.Sprintf("idempotency_key: 1 to %d characters", maxIdempotencyKeyLen))
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '!' || key[i] > '~' {
			return "", svcerr.InvalidArgument("idempotency_key: printable ASCII with no spaces")
		}
	}
	return key, nil
}

// checkFireInputs refuses supplied inputs the activation's workflow would not
// take for this firing. It is the check every activation write applies to its
// params (validation.ValidateActivationParams), over the stored params with
// the supplied inputs on top, plus what only a firing needs:
//
//   - only an activation of a declared trigger has declared inputs to check
//     against;
//   - an input the declaration sets from each event stays the event's;
//   - an input the activation already stores a param for is the owner's
//     approved value, and a firing does not change it;
//   - only plain value inputs (string, number, boolean, enum, array, object)
//     can be supplied: a model, preset, tools, message or integration input
//     chooses what the run may do, which is the activation's to set.
func (s *Service) checkFireInputs(ctx context.Context, trigger *core.Trigger, supplied map[string]any) error {
	if trigger.WorkflowTrigger == nil {
		return svcerr.InvalidArgument("inputs: this trigger declares no inputs to supply")
	}
	decl, err := triggers.ResolveDeclaration(ctx, triggers.LaunchWorkflows{Repo: s.deps.Repo}, trigger)
	if err != nil {
		var broken *triggers.DeclarationError
		if errors.As(err, &broken) {
			return svcerr.FailedPrecondition(broken.Error())
		}
		return triggerDBError("resolve workflow", err)
	}

	merged := make(map[string]any, len(trigger.Params)+len(supplied))
	for name, value := range trigger.Params {
		if decl.WorkflowInputs[name] != nil {
			merged[name] = value
		}
	}
	var problems []string
	for _, name := range sortedNames(supplied) {
		input := decl.WorkflowInputs[name]
		switch {
		case input == nil:
			// ValidateActivationParams says so, and suggests the near miss.
		case isMappedInput(decl, name):
			problems = append(problems, fmt.Sprintf("%q is set from each event by the trigger and cannot be supplied", name))
			continue
		case !isPlainValueInput(input):
			problems = append(problems, fmt.Sprintf("%q chooses what the run may use and cannot be supplied", name))
			continue
		}
		if _, stored := trigger.Params[name]; stored && input != nil {
			problems = append(problems, fmt.Sprintf("%q is set by the activation and cannot be overridden", name))
			continue
		}
		merged[name] = supplied[name]
	}
	if len(problems) == 0 {
		for _, issue := range validation.ValidateActivationParams(decl.WorkflowInputs, decl.Inputs, merged) {
			if _, asked := supplied[issue.Input]; asked {
				problems = append(problems, issue.Message)
			}
		}
	}
	if len(problems) > 0 {
		return svcerr.InvalidArgument("inputs: " + strings.Join(problems, "; "))
	}
	return nil
}

func isMappedInput(decl *triggers.Declaration, name string) bool {
	_, mapped := decl.Inputs[name]
	return mapped
}

func isPlainValueInput(input *reliantv1.Input) bool {
	switch input.GetConfig().(type) {
	case *reliantv1.Input_StringInput, *reliantv1.Input_IntegerInput, *reliantv1.Input_NumberInput,
		*reliantv1.Input_BooleanInput, *reliantv1.Input_EnumInput, *reliantv1.Input_ArrayInput,
		*reliantv1.Input_ObjectInput:
		return true
	}
	return false
}

func sortedNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	// Few, and only for a stable message.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// fireInbound is "run now" for an inbound trigger: a manual event whose
// payload is the marker and the inputs the caller supplied, recorded and
// launched like any delivery.
//
// A key names the event: it is recorded under (trigger, key), so a repeat is
// the intake's duplicate and answers with the first event, whatever inputs it
// carries. Without a key each press is a new event.
func (s *Service) fireInbound(ctx context.Context, trigger *core.Trigger, inputs map[string]any, key string) (*reliantv1.FireTriggerResponse, error) {
	if s.deps.Intake == nil {
		return nil, inboundUnavailable(string(trigger.Kind))
	}
	requester := auth.MustGetUserID(ctx)
	payload := map[string]any{"manual": true, "requested_by": requester}
	if len(inputs) > 0 {
		payload[triggers.PayloadInputs] = inputs
	}
	dedupeKey := trigger.ID + ":manual-" + uuid.NewString()
	if key != "" {
		// The trigger id leads, so one trigger's key can never be another's;
		// the mark keeps it apart from the receivers' own keys.
		dedupeKey = trigger.ID + idempotencyDedupeMark + key
	}
	res, err := s.deps.Intake.Accept(ctx, trigger, triggers.InboundEvent{
		Kind:      trigger.Kind.EventKind(),
		DedupeKey: dedupeKey,
		Payload:   payload,
		// "Run now" is sent by the signed-in person who pressed it, not by
		// the trigger's source.
		Sender: &core.TriggerSender{Kind: core.TriggerSenderKindUser, ID: requester, Verified: true},
	}, triggers.AcceptOptions{Manual: true, ExplicitDedupeKey: key != ""})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("record fire failed"))
	}
	return &reliantv1.FireTriggerResponse{
		FireWorkflowId: triggers.EventFireWorkflowID(res.EventID),
		EventId:        res.EventID,
		ChatId:         res.ChatID,
	}, nil
}
