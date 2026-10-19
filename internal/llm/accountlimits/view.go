// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"strconv"
	"strings"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ProtoAccount is the account as Settings → Providers shows it: identity and
// plan from the account row, status, windows and usage provenance from its
// limit state. limits may be nil (never observed). The wire carries the stored
// windows unchanged; the client applies Effective's rules at render time, so a
// window that reset since it was observed shows as a new window.
func ProtoAccount(a core.ProviderAccount, limits *core.LimitState, now time.Time) *pb.ProviderAccount {
	out := &pb.ProviderAccount{
		Id:                  a.ID,
		Provider:            a.Provider,
		ProviderDisplayName: models.ProviderDisplayName(a.Provider),
		AuthKind:            protoAuthKind(a.AuthKind),
		Label:               a.DisplayLabel(),
		Email:               optional(a.Email),
		OrganizationName:    optional(a.OrganizationName),
		Plan:                optional(a.Plan),
		PlanDisplay:         optional(PlanDisplay(a.Plan)),
		Priority:            int32(a.Priority),
		CreatedAt:           a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.AuthKind == core.AuthKindAPIKey && a.KeyHint != "" {
		out.MaskedKey = optional("…" + a.KeyHint)
	}

	switch a.Status(limits, now) {
	case core.AccountNeedsSignIn:
		out.Status = pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_NEEDS_SIGN_IN
		out.NeedsSignInReason = optional(a.NeedsReauthReason)
	case core.AccountPaused:
		out.Status = pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_PAUSED
	case core.AccountLimited:
		out.Status = pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED
		out.LimitedUntil = optional(limits.LimitedUntil.UTC().Format(time.RFC3339))
		out.LimitedWindow = optional(DisplayWindow(limits.LimitedWindow))
	default:
		out.Status = pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE
	}

	if limits == nil {
		return out
	}
	for _, w := range limits.Windows {
		uw := &pb.UsageWindow{
			Name:          strings.ReplaceAll(w.Name, ".", "_"),
			Label:         WindowLabel(w),
			LengthSeconds: w.LengthS,
		}
		if w.UsedFraction != nil {
			f := *w.UsedFraction
			uw.UsedFraction = &f
		} else if f, ok := windowUsed(w); ok {
			uw.UsedFraction = &f
		}
		if !w.ResetsAt.IsZero() {
			uw.ResetsAt = optional(w.ResetsAt.UTC().Format(time.RFC3339))
		}
		out.Windows = append(out.Windows, uw)
	}
	if !limits.ObservedAt.IsZero() {
		out.UsageObservedAt = optional(limits.ObservedAt.UTC().Format(time.RFC3339))
	}
	out.UsageSource = protoSource(limits.Source, !limits.ObservedAt.IsZero())
	out.UsageError = optional(limits.UsageAPIError)
	return out
}

// WindowLabel is the display label of a window: "5-hour", "Weekly", and
// "Weekly · Opus" for a model-scoped weekly window ("7d.opus"). A Codex extra
// limit family ("codex_other.primary") leads with the family: "Codex other · 5-hour".
func WindowLabel(w core.LimitWindow) string {
	head, tail, scoped := strings.Cut(w.Name, ".")
	if scoped && head != "5h" && head != "7d" {
		return title(head) + " · " + WindowLabel(core.LimitWindow{Name: tail, LengthS: w.LengthS})
	}
	length := time.Duration(w.LengthS) * time.Second
	var base string
	switch {
	case head == "5h", length == 5*time.Hour:
		base = "5-hour"
	case head == "7d", length == 7*24*time.Hour:
		base = "Weekly"
	case length > 0 && length%(24*time.Hour) == 0:
		base = strconv.Itoa(int(length/(24*time.Hour))) + "-day"
	case length > 0 && length%time.Hour == 0:
		base = strconv.Itoa(int(length/time.Hour)) + "-hour"
	default:
		base = title(head)
	}
	if scoped {
		return base + " · " + title(tail)
	}
	return base
}

func title(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "_", " "), "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// PlanDisplay is the badge text of a normalized plan ("max_20x" → "Max 20×").
func PlanDisplay(plan string) string {
	if plan == "" {
		return ""
	}
	parts := strings.Split(plan, "_")
	for i, p := range parts {
		if len(p) > 1 && p[len(p)-1] == 'x' && isDigits(p[:len(p)-1]) {
			parts[i] = p[:len(p)-1] + "×"
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func protoAuthKind(k core.AuthKind) pb.ProviderAuthKind {
	switch k {
	case core.AuthKindOAuth:
		return pb.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH
	case core.AuthKindAPIKey:
		return pb.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY
	case core.AuthKindDevice:
		return pb.ProviderAuthKind_PROVIDER_AUTH_KIND_DEVICE
	case core.AuthKindManaged:
		return pb.ProviderAuthKind_PROVIDER_AUTH_KIND_MANAGED
	}
	return pb.ProviderAuthKind_PROVIDER_AUTH_KIND_UNSPECIFIED
}

// protoSource is where the windows came from. An account that was scheduled for
// polling but never observed has no windows and no source.
func protoSource(source string, observed bool) pb.UsageSource {
	if !observed {
		return pb.UsageSource_USAGE_SOURCE_UNSPECIFIED
	}
	switch source {
	case SourceHeaders:
		return pb.UsageSource_USAGE_SOURCE_HEADERS
	case SourceUsageAPI:
		return pb.UsageSource_USAGE_SOURCE_USAGE_API
	case SourceLimitError:
		return pb.UsageSource_USAGE_SOURCE_LIMIT_ERROR
	}
	return pb.UsageSource_USAGE_SOURCE_UNSPECIFIED
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
