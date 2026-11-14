package grpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// OrgKeyUsagePath is control-plane's read of an organization's org-key usage
// (control-plane internal/app/org_key_usage_wire.go).
const OrgKeyUsagePath = "/internal/org-llm-usage/org-keys"

// orgKeyUsageMaxDays bounds one read: control-plane asks for one month.
const orgKeyUsageMaxDays = 400

// OrgKeyUsageStore sums an org's calls on its own API keys. *db.Repo is one.
type OrgKeyUsageStore interface {
	SumLLMOrgKeyUsage(ctx context.Context, orgID string, from, to time.Time) ([]db.OrgKeyModelUsage, error)
}

type orgKeyUsageRequest struct {
	OrgID     string `json:"org_id"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

type orgKeyUsageRow struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Calls            int64  `json:"calls"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type orgKeyUsageResponse struct {
	OrgID string           `json:"org_id"`
	AsOf  string           `json:"as_of"`
	Rows  []orgKeyUsageRow `json:"rows"`
}

// NewOrgKeyUsageHandler is control-plane's internal-service route onto the
// tokens an organization's OWN API keys used (funding org_key), per provider
// and model over a range of UTC days: what control-plane prices at list for
// its cost summary's byok source (docs/design/org-provider-accounts.md §5,
// P9). Counts only, never content, and never a member's personal account: it
// reads the org rollup of org-funded calls alone.
//
// It accepts only the short-lived internal-service JWT (the one
// /internal/hosted-http/redeem accepts), never a user's credential.
func NewOrgKeyUsageHandler(store OrgKeyUsageStore, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret == "" || store == nil || !validHostedRedeemBearer(r.Header.Get("Authorization"), secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req orgKeyUsageRequest
		if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		from, errFrom := time.Parse(time.DateOnly, req.StartDate)
		to, errTo := time.Parse(time.DateOnly, req.EndDate)
		if strings.TrimSpace(req.OrgID) == "" || errFrom != nil || errTo != nil || to.Before(from) ||
			int(to.Sub(from)/(24*time.Hour))+1 > orgKeyUsageMaxDays {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		rows, err := store.SumLLMOrgKeyUsage(r.Context(), req.OrgID, from, to)
		if err != nil {
			logging.WarnContext(r.Context(), "[org-key-usage] reading the org rollup failed", "org_id", req.OrgID, "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		out := orgKeyUsageResponse{OrgID: req.OrgID, AsOf: time.Now().UTC().Format(time.RFC3339), Rows: make([]orgKeyUsageRow, 0, len(rows))}
		for _, row := range rows {
			out.Rows = append(out.Rows, orgKeyUsageRow{Provider: row.Provider, Model: row.Model, Calls: row.Calls,
				InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
