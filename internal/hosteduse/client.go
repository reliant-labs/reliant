package hosteduse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/observe"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

const ticketTTL = time.Minute
const executePath = "/internal/hosted-http/execute"

// Client issues durable tickets and sends exactly one controller POST. It is
// not registered as an agent tool or a general credential resolver.
type Client struct {
	store    Store
	endpoint string
	sign     func() (string, error)
	http     *http.Client
}

// New constructs a fixed-action client. Verified HTTPS is required outside
// loopback development, including for cluster Service names. Auth tokens are
// minted at use; missing configuration fails closed.
func New(d Deps) (Service, error) {
	baseURL, caFile := d.Worker.GetHostedActionsUrl(), d.Worker.GetHostedActionsCaFile()
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("hosted HTTP controller URL is invalid")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback || caFile != "") {
		return nil, errors.New("hosted HTTP controller requires HTTPS")
	}
	if d.Store == nil || d.Sign == nil {
		return nil, errors.New("hosted HTTP store and signer are required")
	}
	h := *observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 45 * time.Second})
	if d.HTTPClient != nil {
		h = *d.HTTPClient
		if h.Timeout <= 0 {
			h.Timeout = 45 * time.Second
		}
	}
	if caFile != "" {
		transport, err := privateControllerTransport(caFile)
		if err != nil {
			return nil, err
		}
		h.Transport = transport
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{store: d.Store, endpoint: strings.TrimRight(baseURL, "/") + executePath, sign: d.Sign, http: &h}, nil
}

// Issue accepts only the real Temporal Action activity context matching the
// persisted workflow execution. SDK identity, not tool/model parameters, binds
// the call. The DB supplies owner/project/name and checks active run state.
// Activity retries never get a second ticket, even after expiry/redemption.
func (c *Client) Issue(ctx context.Context, runID, nodeID, grantID string, req hostedhttp.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.New("hosted HTTP action was cancelled")
	}
	if !activity.IsActivity(ctx) {
		return "", errors.New("hosted HTTP requires a fixed Action activity")
	}
	info := activity.GetInfo(ctx)
	if info.ActivityType.Name != "Action" || info.WorkflowExecution.ID != runID || runID == "" || info.WorkflowExecution.RunID == "" || info.ActivityID == "" || nodeID == "" || grantID == "" || len(nodeID) > 512 || len(grantID) > 256 || len(runID) > 512 {
		return "", errors.New("hosted HTTP action provenance denied")
	}
	digest, err := hostedhttp.RequestDigest(req)
	if err != nil {
		return "", errors.New("hosted HTTP request is invalid")
	}
	expires := time.Now().UTC().Add(ticketTTL)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	if !expires.After(time.Now()) {
		return "", errors.New("hosted HTTP activity has expired")
	}
	id := uuid.NewString()
	ok, err := c.store.CreateHostedHTTPActionTicket(ctx, core.HostedHTTPActionTicket{ID: id, WorkflowID: runID, TemporalRunID: info.WorkflowExecution.RunID, ActivityID: info.ActivityID, NodeID: nodeID, GrantID: grantID, RequestSHA256: digest, ExpiresAt: expires})
	if err != nil {
		return "", errors.New("hosted HTTP ticket persistence failed")
	}
	if !ok {
		return "", errors.New("hosted HTTP action denied or already attempted; execution will not be repeated")
	}
	return id, nil
}

// ExecuteAction never retries the controller POST. An uncertain transport
// outcome may have sent a mutation; the durable activity tombstone refuses a
// new ticket on any Temporal retry. Even a failed response body is never echoed.
func (c *Client) ExecuteAction(ctx context.Context, runID, nodeID, grantID string, req hostedhttp.Request) (*hostedhttp.Result, error) {
	// Freeze the typed request before hashing, preserving JSON numbers across
	// the internal wire. The caller cannot mutate a map between hash and send.
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > hostedhttp.MaxRequestBytes {
		return nil, errors.New("hosted HTTP request is invalid")
	}
	var frozen hostedhttp.Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err = dec.Decode(&frozen); err != nil {
		return nil, errors.New("hosted HTTP request is invalid")
	}
	// Sign before claiming, so a local configuration failure cannot burn a ticket.
	token, err := c.sign()
	if err != nil || token == "" {
		return nil, errors.New("hosted HTTP service authentication is unavailable")
	}
	ticket, err := c.Issue(ctx, runID, nodeID, grantID, frozen)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		GrantID  string             `json:"grant_id"`
		TicketID string             `json:"ticket_id"`
		Request  hostedhttp.Request `json:"request"`
	}{grantID, ticket, frozen})
	if err != nil {
		return nil, errors.New("hosted HTTP request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("hosted HTTP controller request could not be created")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	// Do not give net/http a replay body, even if transport settings later change.
	request.GetBody = nil
	response, err := c.http.Do(request)
	if err != nil {
		return nil, errors.New("hosted HTTP execution outcome is unknown; request will not be repeated")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("hosted HTTP controller refused execution; request will not be repeated")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(payload) > 8<<20 {
		return nil, errors.New("hosted HTTP controller response is invalid; request will not be repeated")
	}
	var result hostedhttp.Result
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return nil, errors.New("hosted HTTP controller response is invalid; request will not be repeated")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("hosted HTTP controller response is invalid; request will not be repeated")
	}
	// No hosted mutation is automatically retried, including upstream 429/5xx.
	result.Retryable = false
	return &result, nil
}
