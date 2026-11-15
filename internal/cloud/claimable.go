package cloud

// Claimable deploys: `forge env deploy <env> --claimable` for an agent with no
// account. The control plane mints an anonymous identity and a narrow deploy
// token, the CLI deploys with that token, and the claim link it gets back is
// what a human opens to take the deploy over (docs/design/claimable-deploys.md
// in control-plane, §1, §4, §6).
//
// WHAT LIVES HERE: the wire client for the two unauthenticated rpcs, the
// proof-of-work solver, the stored credential, and the override that lets the
// ordinary hosted publish run with that credential. WHAT DOES NOT: the output,
// the exit codes and the eligibility check, which are the CLI's.
//
// THE ANONYMOUS TOKEN CAN NEVER PASS FOR A LOGIN. It is stored under its own
// client id (ClaimableClientIDFor), and ResolveCredential looks up only
// ClientID, so a stored claimable entry is invisible to every command except
// the --claimable path. That path installs the token for ONE process through
// UseClaimableCredential, after checking that no real credential resolves.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// ClaimableClientIDPrefix starts the credentials-file client id of every
// anonymous deploy token. The id is not the bare prefix: a machine can hold one
// claimable deploy per (project, env), and the control plane's idempotency key
// is the same triple, so each gets its own entry (ClaimableClientIDFor).
const ClaimableClientIDPrefix = "forge-cli-claimable"

// ClaimableClientIDFor is the client id the anonymous token of (project, env)
// is stored under. It is never equal to ClientID, which is all that makes a
// stored claimable token unable to stand in for a `forge login`.
func ClaimableClientIDFor(project, env string) string {
	return ClaimableClientIDPrefix + "/" + project + "/" + env
}

// SourceClaimable is the Credential.Source of the anonymous token.
const SourceClaimable CredentialSource = "claimable deploy"

// The reasons (x-forge-error-reason) the ClaimableService answers with, and
// the two access-token reasons that mean the same thing for a stored token.
const (
	ReasonClaimableThrottled   = "claimable_throttled"
	ReasonClaimableUnavailable = "claimable_unavailable"
	ReasonClaimableClaimed     = "claimable_claimed"
	ReasonClaimableExpired     = "claimable_expired"
	ReasonClaimableTakenDown   = "claimable_taken_down"
	ReasonClaimableInvalidPoW  = "claimable_invalid_pow"
)

// The ClaimableService procedures forge calls.
const (
	ProcStartClaimableDeploy     = "controlplane.v1.ClaimableService/StartClaimableDeploy"
	ProcGetClaimableChallenge    = "controlplane.v1.ClaimableService/GetClaimableChallenge"
	ProcGetClaimableAvailability = "controlplane.v1.ClaimableService/GetClaimableAvailability"
	claimableThrottleDetailType  = "controlplane.v1.ClaimableThrottle"
)

// ClaimableChallenge is the proof-of-work puzzle: find a decimal nonce such
// that SHA-256(Challenge || ":" || nonce) has at least DifficultyBits leading
// zero bits.
type ClaimableChallenge struct {
	Challenge      string    `json:"challenge"`
	DifficultyBits uint32    `json:"difficultyBits"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// ClaimableStartRequest is StartClaimableDeploy's request.
type ClaimableStartRequest struct {
	Project      string `json:"project"`
	Env          string `json:"env"`
	DeviceID     string `json:"deviceId"`
	PoWChallenge string `json:"powChallenge"`
	PoWNonce     string `json:"powNonce"`
}

// ClaimableDeploy is StartClaimableDeploy's response: the anonymous deploy
// token, the org it acts for and the claim link.
type ClaimableDeploy struct {
	AccessToken string    `json:"accessToken"`
	OrgID       string    `json:"orgId"`
	ClaimURL    string    `json:"claimUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
	PurgeAfter  time.Time `json:"purgeAfter"`
	Reused      bool      `json:"reused"`
}

// ClaimableCause is one reason claimable deploys, or their compute, are
// unavailable (GetClaimableAvailability, and the metadata of a
// claimable_unavailable refusal).
type ClaimableCause struct {
	// Code is disabled | dependency_unavailable | not_supported.
	Code string `json:"code"`
	// Component is the switch or the dependency (claim_secret,
	// anonymous_signup, content_scan_web_risk, network_policing, ...).
	Component string `json:"component,omitempty"`
	Message   string `json:"message"`
}

// The cause codes (control-plane internal/claimable).
const (
	ClaimableCauseDisabled              = "disabled"
	ClaimableCauseDependencyUnavailable = "dependency_unavailable"
	ClaimableCauseNotSupported          = "not_supported"
	// ClaimableCauseNotDeployed is forge's own: the control plane does not
	// serve the ClaimableService at all.
	ClaimableCauseNotDeployed = "not_deployed"
)

// ClaimableAvailability is GetClaimableAvailability's answer: can a start be
// admitted now (enough for a static site), can an unclaimed service and
// database run, and every reason not.
type ClaimableAvailability struct {
	Available        bool             `json:"available"`
	Causes           []ClaimableCause `json:"causes,omitempty"`
	ComputeAvailable bool             `json:"computeAvailable"`
	ComputeCauses    []ClaimableCause `json:"computeCauses,omitempty"`
	// Limits are the claimable plan's caps, as the control plane enforces
	// them. Nil from a control plane that predates publishing them.
	Limits *ClaimableLimits `json:"limits,omitempty"`
}

// ClaimableLimits is the control plane's ClaimableLimits message (Connect's
// JSON names): what one unclaimed deploy may run. Every value is a hard cap;
// "per workload" holds for the service and, separately, its migration job,
// each request = limit.
type ClaimableLimits struct {
	MaxStaticSites              int32 `json:"maxStaticSites,omitempty"`
	MaxStaticStoredMib          int32 `json:"maxStaticStoredMib,omitempty"`
	MaxServices                 int32 `json:"maxServices,omitempty"`
	MaxReplicas                 int32 `json:"maxReplicas,omitempty"`
	MaxMigrationJobs            int32 `json:"maxMigrationJobs,omitempty"`
	WorkloadCPUMillicores       int32 `json:"workloadCpuMillicores,omitempty"`
	WorkloadMemoryMib           int32 `json:"workloadMemoryMib,omitempty"`
	WorkloadEphemeralStorageMib int32 `json:"workloadEphemeralStorageMib,omitempty"`
	MaxDatabases                int32 `json:"maxDatabases,omitempty"`
	DatabaseInstances           int32 `json:"databaseInstances,omitempty"`
	DatabaseCPUMillicores       int32 `json:"databaseCpuMillicores,omitempty"`
	DatabaseMemoryMib           int32 `json:"databaseMemoryMib,omitempty"`
	DatabaseStorageMib          int32 `json:"databaseStorageMib,omitempty"`
	DatabaseMaxConnections      int32 `json:"databaseMaxConnections,omitempty"`
	DatabaseDeclaredStorageGib  int32 `json:"databaseDeclaredStorageGib,omitempty"`
}

// ClaimableAPI is the unauthenticated half of the ClaimableService. A seam so
// a test states the control plane's answers; production is ClaimableClient.
type ClaimableAPI interface {
	Availability(ctx context.Context) (ClaimableAvailability, error)
	Challenge(ctx context.Context) (ClaimableChallenge, error)
	Start(ctx context.Context, req ClaimableStartRequest) (ClaimableDeploy, error)
}

// ClaimableClient speaks the ClaimableService over Connect's JSON binding,
// with NO credential: the caller has none, and StartClaimableDeploy is how it
// gets one.
type ClaimableClient struct {
	client *Client
}

// NewClaimableClient builds the client for ep.
func NewClaimableClient(ep Endpoint) *ClaimableClient {
	return &ClaimableClient{client: NewClient(ep, Credential{})}
}

// Availability asks whether a claimable deploy can start now. It creates
// nothing and costs no proof of work.
func (c *ClaimableClient) Availability(ctx context.Context) (ClaimableAvailability, error) {
	var out ClaimableAvailability
	if err := c.client.CallAnonymous(ctx, ProcGetClaimableAvailability, map[string]any{}, &out); err != nil {
		return ClaimableAvailability{}, err
	}
	return out, nil
}

// ClaimableUnavailableCauseOf reads the cause a claimable_unavailable refusal
// carries in its metadata. ok is false for any other error, or a refusal from
// a control plane that names no cause.
func ClaimableUnavailableCauseOf(err error) (ClaimableCause, bool) {
	var ce *Error
	if !errors.As(err, &ce) || !ce.HasReason(ReasonClaimableUnavailable) || ce.Header == nil {
		return ClaimableCause{}, false
	}
	code := strings.TrimSpace(ce.Header.Get("x-forge-error-unavailable-cause"))
	if code == "" {
		return ClaimableCause{}, false
	}
	return ClaimableCause{Code: code, Component: strings.TrimSpace(ce.Header.Get("x-forge-error-unavailable-component")), Message: ce.Message}, true
}

// Challenge fetches a fresh proof-of-work puzzle.
func (c *ClaimableClient) Challenge(ctx context.Context) (ClaimableChallenge, error) {
	var out ClaimableChallenge
	if err := c.client.CallAnonymous(ctx, ProcGetClaimableChallenge, map[string]any{}, &out); err != nil {
		return ClaimableChallenge{}, err
	}
	return out, nil
}

// Start mints (or, idempotently, finds) the anonymous deploy.
func (c *ClaimableClient) Start(ctx context.Context, req ClaimableStartRequest) (ClaimableDeploy, error) {
	var out ClaimableDeploy
	if err := c.client.CallAnonymous(ctx, ProcStartClaimableDeploy, req, &out); err != nil {
		return ClaimableDeploy{}, err
	}
	if strings.TrimSpace(out.AccessToken) == "" || strings.TrimSpace(out.ClaimURL) == "" {
		return ClaimableDeploy{}, errors.New("the control plane answered StartClaimableDeploy without an access token and claim link")
	}
	return out, nil
}

// MaxPoWDifficultyBits is the hardest puzzle forge will solve. The server's
// default costs about two seconds; a difficulty far above it is a broken or
// hostile server, and solving it would hang the deploy for hours.
const MaxPoWDifficultyBits = 28

// SolvePoW finds the nonce for ch, spreading the search over the CPUs.
func SolvePoW(ctx context.Context, ch ClaimableChallenge) (string, error) {
	if ch.DifficultyBits > MaxPoWDifficultyBits {
		return "", fmt.Errorf("the control plane asked for a %d-bit proof of work; forge solves at most %d bits. "+
			"Sign up instead: `forge login`", ch.DifficultyBits, MaxPoWDifficultyBits)
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan string, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start int64) {
			defer wg.Done()
			prefix := []byte(ch.Challenge + ":")
			buf := make([]byte, 0, len(prefix)+20)
			for n := start; ; n += int64(workers) {
				if n&0xfff == start&0xfff && ctx.Err() != nil {
					return
				}
				buf = strconv.AppendInt(append(buf[:0], prefix...), n, 10)
				sum := sha256.Sum256(buf)
				if leadingZeroBits(sum[:]) >= int(ch.DifficultyBits) {
					select {
					case found <- strconv.FormatInt(n, 10):
					default:
					}
					return
				}
			}
		}(int64(w))
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case nonce := <-found:
		return nonce, nil
	case <-done:
		select {
		case nonce := <-found:
			return nonce, nil
		default:
		}
		return "", ctx.Err()
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func leadingZeroBits(sum []byte) int {
	n := 0
	for _, b := range sum {
		if b == 0 {
			n += 8
			continue
		}
		return n + bits.LeadingZeros8(b)
	}
	return n
}

// ClaimableThrottle is a refusal to mint, with when to come back.
type ClaimableThrottle struct {
	// Scope is network | device | platform.
	Scope string
	// RetryAfter is how long to wait.
	RetryAfter time.Duration
}

// ClaimableThrottleOf reads a throttle out of err: a ResourceExhausted refusal
// (or one with reason claimable_throttled). The facts come from the
// ClaimableThrottle error detail, and from the metadata the server also sends
// for a client that ignores details.
func ClaimableThrottleOf(err error) (ClaimableThrottle, bool) {
	var ce *Error
	if !errors.As(err, &ce) {
		return ClaimableThrottle{}, false
	}
	if !ce.HasReason(ReasonClaimableThrottled) && !ce.HasCode(CodeResourceExhausted) {
		return ClaimableThrottle{}, false
	}
	var t ClaimableThrottle
	if raw, ok := ce.DetailJSON(claimableThrottleDetailType); ok {
		var d map[string]any
		if json.Unmarshal(raw, &d) == nil {
			t.RetryAfter = seconds(firstOf(d, "retryAfterSeconds", "retry_after_seconds"))
			t.Scope, _ = firstOf(d, "scope").(string)
		}
	}
	if t.RetryAfter <= 0 {
		t.RetryAfter = seconds(ce.Header.Get("x-forge-error-retry-after-seconds"))
	}
	if t.RetryAfter <= 0 {
		t.RetryAfter = seconds(ce.Header.Get("Retry-After"))
	}
	if t.Scope == "" {
		t.Scope = strings.TrimSpace(ce.Header.Get("x-forge-error-throttle-scope"))
	}
	return t, true
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

// seconds reads a protojson int64 (a string) or a plain number as a duration.
func seconds(v any) time.Duration {
	var n int64
	switch x := v.(type) {
	case float64:
		n = int64(x)
	case string:
		n, _ = strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	}
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// ─── The stored credential ───────────────────────────────────────────────────

// LoadClaimable returns the stored anonymous deploy of (project, env) at ep,
// or credentials.ErrNotFound. A stored entry whose token has expired is
// reported as not found: its deploy is gone.
func LoadClaimable(ep Endpoint, project, env string, now time.Time) (credentials.Credential, error) {
	path, err := CredentialsPath()
	if err != nil {
		return credentials.Credential{}, err
	}
	c, err := credentials.Lookup(path, ep.URL, ClaimableClientIDFor(project, env))
	if err != nil {
		return credentials.Credential{}, err
	}
	if c.Claimable == nil || c.Expired(now) {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return c, nil
}

// StoreClaimable records the anonymous deploy of (project, env) at ep.
func StoreClaimable(ep Endpoint, project, env string, d ClaimableDeploy, now time.Time) (credentials.Credential, error) {
	path, err := CredentialsPath()
	if err != nil {
		return credentials.Credential{}, err
	}
	purge := d.PurgeAfter.UTC()
	c := credentials.Credential{
		Token:       d.AccessToken,
		TokenPrefix: displayPrefix(d.AccessToken),
		Scopes:      []string{"deploy:read", "deploy:write"},
		CreatedAt:   now.UTC(),
		Claimable: &credentials.Claimable{
			ClaimURL: d.ClaimURL, OrgID: d.OrgID,
			ExpiresAt: d.ExpiresAt.UTC(), PurgeAfter: purge,
		},
	}
	if !purge.IsZero() {
		c.ExpiresAt = &purge
	}
	return c, credentials.Store(path, ep.URL, ClaimableClientIDFor(project, env), c)
}

// ForgetClaimable drops the stored anonymous deploy of (project, env).
func ForgetClaimable(ep Endpoint, project, env string) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	_, err = credentials.Remove(path, ep.URL, ClaimableClientIDFor(project, env))
	return err
}

// RecordClaimableURL remembers the deploy's live address beside its token, so
// `forge env status --claimable` can print it again without a credential.
func RecordClaimableURL(ep Endpoint, project, env, url string) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	return credentials.Update(path, func(f *credentials.File) error {
		c, err := f.Get(ep.URL, ClaimableClientIDFor(project, env))
		if err != nil || c.Claimable == nil || c.Claimable.URL == url {
			return nil
		}
		c.Claimable.URL = url
		return f.Put(ep.URL, ClaimableClientIDFor(project, env), c)
	})
}

// ClaimableDeviceID is this machine's stable claimable-deploy identifier,
// generated once and kept beside the credentials file. Only its hash is stored
// server-side; it is the unit of the "3 live per device" limit and part of the
// idempotency key, so it must not change between runs.
func ClaimableDeviceID() (string, error) {
	credPath, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(credPath), "claimable-device-id")
	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			return id, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a device id: %w", err)
	}
	id := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := atomicfile.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return id, nil
}

// ─── The process-scoped override ─────────────────────────────────────────────

var claimableOverride struct {
	sync.Mutex
	url  string
	cred Credential
	set  bool
}

// UseClaimableCredential makes ResolveCredential return the anonymous token
// for ep, for this process, until the returned function runs. It is how the
// ordinary hosted publish (a dozen call sites that each resolve a credential)
// runs as the anonymous user without any of them learning about claimable
// deploys.
//
// It is installed only by the --claimable path, and only after a real
// credential was found NOT to resolve, so it can never shadow a login.
func UseClaimableCredential(ep Endpoint, token string, expiresAt *time.Time) (restore func()) {
	key, err := credentials.Normalize(ep.URL)
	if err != nil {
		key = ep.URL
	}
	claimableOverride.Lock()
	prev := struct {
		url  string
		cred Credential
		set  bool
	}{claimableOverride.url, claimableOverride.cred, claimableOverride.set}
	claimableOverride.url, claimableOverride.set = key, true
	claimableOverride.cred = Credential{Token: token, Source: SourceClaimable,
		From: "--claimable (anonymous deploy token)", ExpiresAt: expiresAt,
		Scopes: []string{"deploy:read", "deploy:write"}}
	claimableOverride.Unlock()
	return func() {
		claimableOverride.Lock()
		claimableOverride.url, claimableOverride.cred, claimableOverride.set = prev.url, prev.cred, prev.set
		claimableOverride.Unlock()
	}
}

// ClaimableActive reports whether this process is deploying as the anonymous
// claimable user (UseClaimableCredential is installed). The claimable plan is
// platform-funded and never billed, so rules that exist only to keep a bill
// equal to a request (the hosted shape band) do not apply under it.
func ClaimableActive() bool {
	claimableOverride.Lock()
	defer claimableOverride.Unlock()
	return claimableOverride.set
}

func claimableCredentialFor(ep Endpoint) (Credential, bool) {
	claimableOverride.Lock()
	defer claimableOverride.Unlock()
	if !claimableOverride.set {
		return Credential{}, false
	}
	key, err := credentials.Normalize(ep.URL)
	if err != nil || key != claimableOverride.url {
		return Credential{}, false
	}
	return claimableOverride.cred, true
}

// CallAnonymous invokes one Connect procedure with NO Authorization header and
// none of Call's credential refresh or scope elevation: there is no credential
// to refresh.
func (c *Client) CallAnonymous(ctx context.Context, procedure string, req, out any) error {
	return c.call(ctx, procedure, req, out, "")
}
