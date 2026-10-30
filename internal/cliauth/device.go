package cliauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/observe"
)

// DeviceGrantType is RFC 8628 §3.4's grant_type.
const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// ErrNoDeviceFlow reports an authorization server that does not advertise
// RFC 8628 device authorization. The caller falls back to a browser login
// where one can work.
var ErrNoDeviceFlow = errors.New("the control plane does not offer device-code sign-in")

// DeviceLogin is RFC 8628 device authorization: it asks the control plane for
// a short code, prints it with the page that approves it, and polls until a
// signed-in person approves (or declines) in the web app — on any device.
//
// Nothing has to travel back to this process — no loopback listener, no
// browser on this machine — so it works on a cloud machine or a server reached
// over ssh as well as on a laptop. Two commands use it, and the requested
// scopes say which (the control plane derives its prompt the same way):
//
//   - `reliant daemon start` pairs a self-hosted machine (daemon:connect):
//     "Approve machine <hostname>?", also answerable from the web app's
//     waiting Add machine sheet.
//   - `reliant auth login` signs the CLI in where its loopback callback cannot
//     reach a browser: "Sign in the Reliant CLI on <hostname>?".
type DeviceLogin struct {
	// Server is the reliant API server; the authorization server is
	// discovered from it, as for Login.
	Server string
	Scopes []string
	// Name labels the minted token (<client>@<name>).
	Name string
	// Hostname and Platform describe this machine on the approval prompt.
	Hostname string
	Platform string
	// Out receives the code and the URL.
	Out io.Writer
	// OpenURL, when set, is tried once with the approval URL. Failure is
	// ignored: the printed code is the flow, the browser is a convenience.
	OpenURL func(string) error
	// HTTPClient performs discovery, the authorization request and the
	// polls; nil uses bounded defaults.
	HTTPClient oauth2.HTTPDoer
	// Sleep waits between polls; nil uses a context-aware timer. Tests
	// replace it so a poll loop costs no wall clock.
	Sleep func(context.Context, time.Duration) error
}

type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

type tokenReply struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Run waits for the approval and returns the credential. Like Login.Run it does not
// store it. ErrNoDeviceFlow when the authorization server does not offer it.
func (l DeviceLogin) Run(ctx context.Context) (credentials.Credential, error) {
	client := l.HTTPClient
	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: oauth2.DefaultExchangeTimeout})
	}
	sleep := l.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	out := l.Out
	if out == nil {
		out = io.Discard
	}

	meta, err := oauth2.DiscoverAuthorizationServer(ctx, client, l.Server)
	if errors.Is(err, oauth2.ErrNoAuthorizationServer) {
		return credentials.Credential{}, fmt.Errorf("%s does not offer sign-in (%w)\n"+
			"it must publish %s naming its control plane; for a self-hosted server, "+
			"mint a token there and pass it with RELIANT_TOKEN", l.Server, err, oauth2.AuthorizationServerMetadataPath)
	}
	if err != nil {
		return credentials.Credential{}, err
	}
	endpoint, err := deviceEndpoint(meta)
	if err != nil {
		return credentials.Credential{}, err
	}

	pairing := l.pairsMachine()
	what := "device sign-in"
	if pairing {
		what = "device pairing"
	}

	form := url.Values{
		"client_id": {ClientID},
		"scope":     {strings.Join(l.Scopes, " ")},
		"device":    {l.Name},
		"hostname":  {l.Hostname},
		"platform":  {l.Platform},
	}
	var auth deviceAuthorization
	status, err := postForm(ctx, client, endpoint, form, &auth)
	if err != nil {
		return credentials.Credential{}, fmt.Errorf("%s: %w", what, err)
	}
	if status != http.StatusOK || auth.DeviceCode == "" || auth.UserCode == "" {
		return credentials.Credential{}, fmt.Errorf("%s: %s answered HTTP %d", what, endpoint, status)
	}

	approveURL := auth.VerificationURIComplete
	if approveURL == "" {
		approveURL = auth.VerificationURI
	}
	if pairing {
		fmt.Fprintf(out, "\nPair this machine with your Reliant account.\n\n")
	} else {
		fmt.Fprintf(out, "\nSign in the Reliant CLI on %s.\n\n", hostOrThisMachine(l.Hostname))
	}
	fmt.Fprintf(out, "  Code:     %s\n", auth.UserCode)
	fmt.Fprintf(out, "  Approve:  %s\n\n", approveURL)
	if pairing {
		fmt.Fprintf(out, "Or type the code into the Add machine window waiting in Reliant.\n")
	} else {
		fmt.Fprintf(out, "Open the link on any device you can sign in to Reliant on, and check the code matches.\n")
	}
	fmt.Fprintf(out, "Waiting for approval...\n")
	if l.OpenURL != nil {
		_ = l.OpenURL(approveURL)
	}

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ttl := time.Duration(auth.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	deadline := time.Now().Add(ttl)

	for {
		if err := sleep(ctx, interval); err != nil {
			return credentials.Credential{}, err
		}
		var reply tokenReply
		status, err := postForm(ctx, client, meta.TokenEndpoint, url.Values{
			"grant_type":  {DeviceGrantType},
			"client_id":   {ClientID},
			"device_code": {auth.DeviceCode},
		}, &reply)
		if err != nil {
			// A dropped poll is not a failed pairing: the request is still
			// waiting server-side, so try again until it expires.
			if time.Now().After(deadline) {
				return credentials.Credential{}, fmt.Errorf("%s: %w", what, err)
			}
			continue
		}
		if status == http.StatusOK && reply.AccessToken != "" {
			fmt.Fprintln(out, "Approved.")
			return credentialFrom(reply, meta.Issuer), nil
		}
		switch reply.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second // RFC 8628 §3.5
		case "access_denied":
			return credentials.Credential{}, fmt.Errorf("the %s was declined in the web app", what)
		case "expired_token":
			return credentials.Credential{}, errors.New("the code expired before it was approved; run the command again")
		default:
			return credentials.Credential{}, fmt.Errorf("%s: %s: %s", what, reply.Error, reply.ErrorDescription)
		}
	}
}

// pairsMachine reports a machine pairing rather than a CLI sign-in. Keyed on
// the requested authority, as the control plane keys its prompt
// (DeviceRequest.Kind there), so this terminal and the approval page always
// describe the same request.
func (l DeviceLogin) pairsMachine() bool { return slices.Contains(l.Scopes, ScopeDaemon) }

func hostOrThisMachine(hostname string) string {
	if hostname == "" {
		return "this machine"
	}
	return hostname
}

// deviceEndpoint reads device_authorization_endpoint from the metadata and
// holds it to the same rule discovery holds the other endpoints to: on the
// issuer's origin, and https unless loopback.
func deviceEndpoint(meta *oauth2.ProviderMetadata) (string, error) {
	raw, _ := meta.Raw["device_authorization_endpoint"].(string)
	if raw == "" {
		return "", ErrNoDeviceFlow
	}
	u, err := url.Parse(raw)
	issuer, ierr := url.Parse(meta.Issuer)
	if err != nil || ierr != nil || !u.IsAbs() || u.Scheme != issuer.Scheme || u.Host != issuer.Host {
		return "", fmt.Errorf("device_authorization_endpoint %q is not on the issuer's origin %s", raw, meta.Issuer)
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return "", fmt.Errorf("device_authorization_endpoint %q must use https", raw)
	}
	return raw, nil
}

func isLoopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func postForm(ctx context.Context, client oauth2.HTTPDoer, endpoint string, form url.Values, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return resp.StatusCode, fmt.Errorf("%s answered HTTP %d with a body that is not JSON", endpoint, resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func credentialFrom(reply tokenReply, issuer string) credentials.Credential {
	now := time.Now().UTC()
	c := credentials.Credential{
		Token:     reply.AccessToken,
		Scopes:    strings.Fields(reply.Scope),
		CreatedAt: now,
		Issuer:    issuer,
	}
	if len(reply.AccessToken) > 13 {
		c.TokenPrefix = reply.AccessToken[:13]
	}
	if reply.ExpiresIn > 0 {
		exp := now.Add(time.Duration(reply.ExpiresIn) * time.Second)
		c.ExpiresAt = &exp
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
