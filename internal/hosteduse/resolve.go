package hosteduse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
)

const (
	resolvePath         = "/internal/secrets/resolve"
	maxResolveRefs      = 32
	maxResolveResponse  = 4 << 20
	maxResolveTries     = 3
	resolveRetryBackoff = 300 * time.Millisecond
)

// resolveActivityTypes are the activities that may resolve declared secrets:
// only the action step, which puts a value into a request's credential slot. The
// ticket is still bound to the run, the node and the refs, so this only names
// who may ask.
var resolveActivityTypes = map[string]bool{"Action": true}

var (
	refEnvironment = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,256}$`)
	refName        = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)
)

// ResolveSecrets asks the controller for the values of the declared secrets a
// step needs. The values exist only in the returned map, for the life of the
// activity attempt; nothing here logs, persists or returns them anywhere else.
//
// The controller redeems a one-use ticket to learn the trusted run identity
// (owner, project, workflow name, run, node) and checks every ref against the
// secret's access list before reading any value. The ticket is bound to the
// digest of the refs, so it cannot be spent on a different set.
//
// Resolving only reads, so unlike the HTTP send path it may be repeated: each
// try gets a FRESH ticket (a failure after redeem burns the old one), keyed by
// the attempt and the try so the tickets table's one-per-activity tombstone
// does not stop it. A refusal is never retried.
func (c *Client) ResolveSecrets(ctx context.Context, runID, nodeID string, refs []stepsecrets.Ref) (map[stepsecrets.Ref]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("secret resolution was cancelled")
	}
	if !activity.IsActivity(ctx) {
		return nil, errors.New("secret resolution requires a step activity")
	}
	info := activity.GetInfo(ctx)
	if !resolveActivityTypes[info.ActivityType.Name] || info.WorkflowExecution.ID != runID || runID == "" ||
		info.WorkflowExecution.RunID == "" || info.ActivityID == "" || nodeID == "" || len(nodeID) > 512 || len(runID) > 512 {
		return nil, errors.New("secret resolution provenance denied")
	}
	canonical := stepsecrets.CanonicalRefs(refs)
	if len(canonical) == 0 || len(canonical) > maxResolveRefs {
		return nil, fmt.Errorf("a step may use between 1 and %d distinct secrets", maxResolveRefs)
	}
	for _, r := range canonical {
		if !refEnvironment.MatchString(r.Environment) || !refName.MatchString(r.Name) {
			return nil, errors.New("secret reference is malformed")
		}
	}
	digest := stepsecrets.RefsDigest(canonical)

	var lastErr error
	for try := 0; try < maxResolveTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return nil, errors.New("secret resolution was cancelled")
			case <-time.After(time.Duration(try) * resolveRetryBackoff):
			}
		}
		values, retryable, err := c.resolveOnce(ctx, info, runID, nodeID, canonical, digest, try)
		if err == nil {
			return values, nil
		}
		lastErr = err
		if !retryable {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) resolveOnce(ctx context.Context, info activity.Info, runID, nodeID string, refs []stepsecrets.Ref, digest string, try int) (map[stepsecrets.Ref]string, bool, error) {
	// Sign before claiming, so a local configuration failure cannot burn a ticket.
	token, err := c.sign()
	if err != nil || token == "" {
		return nil, false, errors.New("secret resolution authentication is unavailable")
	}
	expires := time.Now().UTC().Add(ticketTTL)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	if !expires.After(time.Now()) {
		return nil, false, errors.New("secret resolution activity has expired")
	}
	id := uuid.NewString()
	// The activity key carries the attempt and try: a retry is a new ticket, and
	// none of them collides with the HTTP send path's own tombstone.
	key := info.ActivityID + "#resolve-" + strconv.Itoa(int(info.Attempt)) + "-" + strconv.Itoa(try)
	ok, err := c.store.CreateHostedHTTPActionTicket(ctx, core.HostedHTTPActionTicket{
		ID: id, WorkflowID: runID, TemporalRunID: info.WorkflowExecution.RunID, ActivityID: key,
		NodeID: nodeID, GrantID: "", RequestSHA256: digest, ExpiresAt: expires,
	})
	if err != nil {
		return nil, true, errors.New("secret resolution ticket persistence failed")
	}
	if !ok {
		return nil, false, errors.New("secret resolution denied: the run is not active")
	}

	payload, err := json.Marshal(struct {
		TicketID string            `json:"ticket_id"`
		Refs     []stepsecrets.Ref `json:"refs"`
	}{id, refs})
	if err != nil {
		return nil, false, errors.New("secret reference is malformed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, errors.New("secret resolution request could not be created")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.GetBody = nil
	response, err := c.http.Do(request)
	if err != nil {
		return nil, true, errors.New("secret resolution is unavailable")
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		// One printable line naming the fix, written for the workflow's author.
		if reason, ok := readRefusal(response.Body); ok {
			return nil, false, &stepsecrets.DeniedError{Reason: reason}
		}
		return nil, false, errors.New("secret resolution was refused")
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return nil, true, errors.New("secret resolution is unavailable")
	default:
		return nil, false, errors.New("secret resolution failed")
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResolveResponse+1))
	if err != nil || len(raw) > maxResolveResponse {
		return nil, true, errors.New("secret resolution response is invalid")
	}
	var out struct {
		Values []struct {
			Environment string `json:"environment"`
			Name        string `json:"name"`
			Value       string `json:"value"`
			Version     int64  `json:"version"`
		} `json:"values"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil || dec.Decode(new(any)) != io.EOF {
		return nil, false, errors.New("secret resolution response is invalid")
	}
	values := make(map[stepsecrets.Ref]string, len(out.Values))
	for _, v := range out.Values {
		values[stepsecrets.Ref{Environment: v.Environment, Name: v.Name}] = v.Value
	}
	for _, want := range refs {
		if _, ok := values[want]; !ok {
			return nil, false, errors.New("secret resolution response is incomplete")
		}
	}
	return values, false, nil
}

// maxRefusalBytes bounds a controller refusal that is shown to the author.
const maxRefusalBytes = 1024

// genericDenial is the controller's catch-all; it says nothing worth showing.
const genericDenial = "hosted HTTP use denied or unavailable"

// readRefusal reads a bounded single-line refusal. The generic denial text is
// reported as absent so the caller says the generic thing itself.
func readRefusal(body io.Reader) (string, bool) {
	raw, err := io.ReadAll(io.LimitReader(body, maxRefusalBytes+1))
	if err != nil || len(raw) > maxRefusalBytes {
		return "", false
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == genericDenial {
		return "", false
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return text, true
}
