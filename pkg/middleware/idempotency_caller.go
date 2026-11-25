package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// IdempotencyInterceptorWithCaller is [IdempotencyInterceptor] with its
// cache scoped to the authenticated caller and every authority that caller
// presented — the partition [IdempotencyInterceptorV2] uses, so a v1 chain
// can have the same guarantee. A unary request whose (caller, procedure,
// Idempotency-Key) triple was answered within the TTL gets the cached
// response or error without the handler running again; streaming calls pass
// through.
//
// The partition is the same as the v2 interceptor's (see
// [WithIdempotencyPartition] for replacing it), and so are the bypass
// rules: a call whose partition cannot be established — claimsFrom reports
// no principal, nil claims, an empty UserID, claims.Raw that has no
// canonical encoding, or a partition func that declines — is neither cached
// nor replayed, and the handler runs as if no key were sent. Put the
// interceptor after authentication in the chain, so the claims are there.
//
// Returns nil when opts.CacheSize <= 0 or claimsFrom is nil.
func IdempotencyInterceptorWithCaller(opts IdempotencyOptions, claimsFrom ClaimsLookup, callerOpts ...IdempotencyCallerOption) connect.Interceptor {
	if opts.CacheSize <= 0 || claimsFrom == nil {
		return nil
	}
	if opts.TTL <= 0 {
		opts.TTL = time.Hour
	}
	return &idempotencyInterceptor{
		cache: newLRUCache[*cachedRPCResponse](opts.CacheSize),
		ttl:   opts.TTL,
		scope: newCallerScope(claimsFrom, callerOpts),
	}
}

// IdempotencyCallerOption configures the caller-scoped idempotency
// interceptors, [IdempotencyInterceptorWithCaller] and
// [IdempotencyInterceptorV2].
type IdempotencyCallerOption func(*callerScope)

// WithIdempotencyPartition names the cache partition a caller's answers
// are stored in and replayed from, in place of the default — a digest of
// every authority the caller's claims carry (the fixed fields, the
// credential, and Raw minus iat/exp/nbf/jti). The interceptor consults fn
// only after it has found a principal with a non-empty UserID; the
// procedure and Idempotency-Key are still part of the key.
//
// Two calls share cached answers exactly when fn returns the same string
// for both, so it must distinguish every authority the handler checks: a
// replay skips the handler, and with it the handler's permission check.
// Returning ok=false, or "", bypasses the cache for that call: the handler
// runs, and nothing is stored or replayed. A nil fn keeps the default.
func WithIdempotencyPartition(fn func(ctx context.Context, claims *auth.Claims) (partition string, ok bool)) IdempotencyCallerOption {
	return func(s *callerScope) { s.partition = fn }
}

// callerScope derives the caller-scoped idempotency cache key that
// [IdempotencyInterceptorWithCaller] and [IdempotencyInterceptorV2] share,
// so the two majors partition — and bypass — identically.
type callerScope struct {
	claimsFrom ClaimsLookup
	partition  func(ctx context.Context, claims *auth.Claims) (string, bool)
}

func newCallerScope(claimsFrom ClaimsLookup, opts []IdempotencyCallerOption) *callerScope {
	s := &callerScope{claimsFrom: claimsFrom}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Domain tags keep a custom partition string from ever equalling the
// default partition's digest, and version the key composition.
const (
	idempotencyPartitionDefault = "forge.idempotency.claims.v1"
	idempotencyPartitionCustom  = "forge.idempotency.custom.v1"
)

// cacheKey is the cache key of one caller's (procedure, key) pair, or ""
// when no partition can be established — and "" means bypass the cache:
// the handler runs, nothing is stored and nothing is replayed. There is no
// shared fallback key.
//
// The partition is, by default, [claimsPartition] of the caller's claims:
// a digest of every authority the principal carries. When
// [WithIdempotencyPartition] set a func its string replaces that digest
// (and ok=false or "" bypasses). Either way the claims lookup must report a
// principal with a non-empty UserID first. The key hashes (domain tag,
// partition, procedure, client key), each length-prefixed, so no choice of
// values can make two different tuples collide.
func (s *callerScope) cacheKey(ctx context.Context, procedure, clientKey string) string {
	claims, ok := s.claimsFrom(ctx)
	if !ok || claims == nil || claims.UserID == "" {
		return ""
	}
	tag, partition := idempotencyPartitionDefault, ""
	if s.partition != nil {
		tag = idempotencyPartitionCustom
		if partition, ok = s.partition(ctx, claims); !ok || partition == "" {
			return ""
		}
	} else if partition, ok = claimsPartition(claims); !ok {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{tag, partition, procedure, clientKey} {
		writeLengthPrefixed(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// volatileClaims are the registered JWT claims that change every time the
// same authority is re-issued — a refreshed token — and carry no authority
// themselves (RFC 7519 §4.1: issued-at, expiry, not-before, token id). They
// are the only keys of claims.Raw the default partition leaves out.
var volatileClaims = map[string]bool{"iat": true, "exp": true, "nbf": true, "jti": true}

// claimsPartition digests every authority claims carries, or reports false
// when that cannot be done canonically. The digest covers, each
// length-prefixed:
//
//   - the fixed fields: UserID, OrgID, Email, EmailVerified, Role, and Roles
//     sorted (each role prefixed by its own length, after their count);
//   - the credential: its kind and an API key's lookup prefix;
//   - claims.Raw minus [volatileClaims], canonically encoded: encoding/json
//     writes every map with its keys sorted, at every depth, and a number
//     in its one shortest round-tripping form, so equal claim sets encode
//     to equal bytes. Raw that json cannot encode (a NaN or ±Inf, a func, a
//     channel) has no canonical form, so the caller has no partition.
//
// So two tokens for one user and org that differ only in a permission held
// in Raw — Clerk's org_permissions, Auth0's app_metadata — never share an
// entry, while a refreshed token of the same authority, differing only in
// iat/exp/nbf/jti, does.
func claimsPartition(claims *auth.Claims) (string, bool) {
	raw := make(map[string]any, len(claims.Raw))
	for k, v := range claims.Raw {
		if !volatileClaims[k] {
			raw[k] = v
		}
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return "", false
	}
	roles := slices.Clone(claims.Roles)
	slices.Sort(roles)

	h := sha256.New()
	for _, part := range []string{
		claims.UserID, claims.OrgID,
		claims.Email, strconv.FormatBool(claims.EmailVerified),
		claims.Role, strconv.Itoa(len(roles)),
	} {
		writeLengthPrefixed(h, part)
	}
	for _, role := range roles {
		writeLengthPrefixed(h, role)
	}
	writeLengthPrefixed(h, claims.Credential.Kind().String())
	writeLengthPrefixed(h, claims.Credential.KeyPrefix())
	writeLengthPrefixed(h, string(rawJSON))
	return hex.EncodeToString(h.Sum(nil)), true
}

// writeLengthPrefixed writes part to h after its 8-byte big-endian length.
func writeLengthPrefixed(h hash.Hash, part string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(part)))
	h.Write(n[:])
	h.Write([]byte(part))
}
