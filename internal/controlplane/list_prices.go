package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// LLMPriceInternalService (control-plane), withheld from the exported
// contract like OrgNotifyInternalService: Connect unary over plain HTTP+JSON,
// signed with this process's internal-service identity.
const getModelListPricesPath = "/controlplane.v1.LLMPriceInternalService/GetModelListPrices"

// MaxListPriceModels is the most models one GetModelListPrices may name
// (control-plane's llm_price_internal.MaxModels).
const MaxListPriceModels = 500

// ErrListPricesUnsupported is a control plane without LLMPriceInternalService,
// or an adapter with no internal-service identity to call it with.
var ErrListPricesUnsupported = errors.New("controlplane: list prices are not available")

// ModelRef names one model as a usage row records it.
type ModelRef struct {
	Provider, Model string
}

// ModelListPrice is one model's list rates, in USD nanos per million tokens of
// each class. Priced false: the catalog has no price for it, and every rate
// is zero meaning "unknown", never "free".
type ModelListPrice struct {
	Ref        ModelRef
	Priced     bool
	CatalogKey string
	// Rates in USD nanos (1e-9 USD) per million tokens.
	InputNanosPerMTok, OutputNanosPerMTok, CacheReadNanosPerMTok, CacheWriteNanosPerMTok int64
}

// GetModelListPrices is Service.GetModelListPrices.
func (c *client) GetModelListPrices(ctx context.Context, refs []ModelRef) ([]ModelListPrice, time.Time, error) {
	if len(refs) == 0 {
		return nil, time.Time{}, nil
	}
	if len(refs) > MaxListPriceModels {
		return nil, time.Time{}, fmt.Errorf("controlplane: %d models; the most one list-price read may name is %d", len(refs), MaxListPriceModels)
	}
	models := make([]map[string]string, len(refs))
	for i, r := range refs {
		models[i] = map[string]string{"provider": r.Provider, "model": r.Model}
	}
	// int64 fields travel as JSON strings (proto3 JSON); a zero is omitted.
	var out struct {
		Prices []struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Priced     bool   `json:"priced"`
			CatalogKey string `json:"catalogKey"`
			Input      json64 `json:"inputUsdNanosPerMillionTokens"`
			Output     json64 `json:"outputUsdNanosPerMillionTokens"`
			CacheRead  json64 `json:"cacheReadUsdNanosPerMillionTokens"`
			CacheWrite json64 `json:"cacheWriteUsdNanosPerMillionTokens"`
		} `json:"prices"`
		CatalogFetchedAt *time.Time `json:"catalogFetchedAt"`
	}
	err := c.projectOrgMoveCall(ctx, getModelListPricesPath, map[string]any{"models": models}, &out)
	if errors.Is(err, ErrProjectOrgMoveUnsupported) {
		return nil, time.Time{}, ErrListPricesUnsupported
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(out.Prices) != len(refs) {
		return nil, time.Time{}, fmt.Errorf("controlplane: list prices answered %d models of %d", len(out.Prices), len(refs))
	}
	prices := make([]ModelListPrice, len(refs))
	for i, p := range out.Prices {
		// The answer is in request order; one that is not is no answer.
		if p.Model != refs[i].Model || p.Provider != refs[i].Provider {
			return nil, time.Time{}, fmt.Errorf("controlplane: list price %d is for %s/%s, asked %s/%s", i, p.Provider, p.Model, refs[i].Provider, refs[i].Model)
		}
		prices[i] = ModelListPrice{Ref: refs[i], Priced: p.Priced, CatalogKey: p.CatalogKey}
		if p.Priced {
			prices[i].InputNanosPerMTok, prices[i].OutputNanosPerMTok = int64(p.Input), int64(p.Output)
			prices[i].CacheReadNanosPerMTok, prices[i].CacheWriteNanosPerMTok = int64(p.CacheRead), int64(p.CacheWrite)
		}
	}
	var fetchedAt time.Time
	if out.CatalogFetchedAt != nil {
		fetchedAt = out.CatalogFetchedAt.UTC()
	}
	return prices, fetchedAt, nil
}

// json64 is a proto3-JSON int64: a decimal string, or (leniently) a number.
type json64 int64

func (v *json64) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "null" || s == "" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("int64 %s: %w", b, err)
	}
	*v = json64(n)
	return nil
}
