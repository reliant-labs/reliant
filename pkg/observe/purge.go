package observe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"unicode/utf8"
)

// Purging one subject's product events (account deletion, a data-subject
// request). The app asks; the platform that stores the events does the
// deleting. The request is itself a product event, sent through the app's
// existing telemetry pipeline, so there is no new endpoint or credential:
//
//   - the record is event.name = "product.forge_purge_requested" with the
//     subject in props (purge.user_id, purge.visitor_id) and a request id
//     (purge.request_id). It names no actor (no user.id, org.id or
//     visitor.id), so it is never anyone's activity;
//   - the Reliant control plane acts on it only when it arrived through the
//     environment's own server-side ingest (a workload's OTLP token or the
//     cluster's log agent), never through a publishable browser or CLI key,
//     and it deletes only that environment's rows in that org's
//     product_events table: the gateway stamps the org and environment, so a
//     request cannot name another's;
//   - the control plane records each request once by (org, environment,
//     request id), so a retry, or the same record delivered twice, purges
//     once. RequestPurge mints a random id when the caller passes none;
//     pass one derived from the deletion itself (the account id) to make a
//     retried call the same request.
//
// The purge covers the long-retention product_events table. Raw logs and
// traces keep their own short retention. Stop emitting events for the
// subject first: an event stored after the purge ran is not purged.
const (
	// EventPurgeRequested is the purge request's event name. The forge_
	// prefix is forge's own.
	EventPurgeRequested = "forge_purge_requested"
	// PropPurgeRequestID, PropPurgeUserID and PropPurgeVisitorID are its
	// props (stored as Props['purge.request_id'] and so on).
	PropPurgeRequestID = "purge.request_id"
	PropPurgeUserID    = "purge.user_id"
	PropPurgeVisitorID = "purge.visitor_id"
)

// PurgeSubject is whose events to delete: the user id the app's events carry
// as user.id, the visitor id they carry as visitor.id (a browser at the full
// analytics tier, a CLI's install id), or both. A row matching either is
// deleted.
type PurgeSubject struct {
	UserID    string
	VisitorID string
}

// ErrInvalidPurge is RequestPurge's refusal of a request it cannot send.
var ErrInvalidPurge = errors.New("observe: invalid purge request")

var purgeRequestIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// RequestPurge asks the platform to delete every product event of subject
// in this environment, and returns the request id it sent. requestID ""
// mints a random one; otherwise it is the caller's (1-128 letters, digits,
// '_', '.', ':' and '-'), and sending it again is the same request.
//
// It writes one INFO record through the context's logger (FromContext), the
// pipeline every product event takes, and is never refused by a
// ConsentSource or the props' PII guard: a purge is how withdrawn consent is
// honoured, and the subject ids are the point. It fails only on a request
// that cannot be honoured: no subject, an id longer than
// MaxProductPropValueLen (the pipeline would truncate it and miss the rows),
// or a malformed request id.
//
//	id, err := observe.RequestPurge(ctx, "account-"+accountID, observe.PurgeSubject{UserID: userID})
func RequestPurge(ctx context.Context, requestID string, subject PurgeSubject) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if subject.UserID == "" && subject.VisitorID == "" {
		return "", fmt.Errorf("%w: name the subject's user id, visitor id or both", ErrInvalidPurge)
	}
	for _, id := range []string{subject.UserID, subject.VisitorID} {
		if utf8.RuneCountInString(id) > MaxProductPropValueLen {
			return "", fmt.Errorf("%w: a subject id is longer than %d characters", ErrInvalidPurge, MaxProductPropValueLen)
		}
	}
	if requestID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("observe: minting a purge request id: %w", err)
		}
		requestID = hex.EncodeToString(b[:])
	}
	if !purgeRequestIDRE.MatchString(requestID) {
		return "", fmt.Errorf("%w: request id %q must be 1-128 letters, digits, '_', '.', ':' and '-'", ErrInvalidPurge, requestID)
	}
	attrs := []slog.Attr{
		slog.String(AttrEventName, EventNamePrefix+EventPurgeRequested),
		slog.String(AttrAppClient, AppClientBackend),
	}
	if v := appVersion.Load(); v != nil && *v != "" {
		attrs = append(attrs, slog.String(AttrAppVersion, *v))
	}
	attrs = append(attrs, slog.String(AttrPropPrefix+PropPurgeRequestID, requestID))
	if subject.UserID != "" {
		attrs = append(attrs, slog.String(AttrPropPrefix+PropPurgeUserID, subject.UserID))
	}
	if subject.VisitorID != "" {
		attrs = append(attrs, slog.String(AttrPropPrefix+PropPurgeVisitorID, subject.VisitorID))
	}
	FromContext(ctx).LogAttrs(ctx, slog.LevelInfo, EventNamePrefix+EventPurgeRequested, attrs...)
	return requestID, nil
}
