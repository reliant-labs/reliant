package ledgerfile

// Importing another ledger's records, with their provenance.
//
// ImportPromotion (promotions.go) already records a promotion verbatim. What
// this adds is the SOURCE: which file, at which blob, the line came from.
// The hosted ledger has an `imported_from` column for exactly this, and a
// machine ledger that dropped the fact would make "where did this history
// come from" unanswerable on the one backend where nobody can ask a server.
//
// WHY THE FIELD IS FLATTENED INTO THE LINE RATHER THAN ADDED TO
// release.Promotion. A promotion is the SHARED vocabulary of both backends,
// and `imported_from` is not a property of a promotion — it is a property of
// this store's copy of one. Putting it on the struct would make every
// promotion forge ever writes carry an always-empty field, and would mean
// the hosted ledger's column and the struct field could disagree about which
// is authoritative. The gateLine precedent in promotions.go is the same
// shape for the same reason: one extra key beside a record's own fields,
// which keeps the line readable AS that record (encoding/json ignores the
// key it does not know), so ImportLedger and every existing reader still
// decode it unchanged.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// ImportPromotion's provenance key, on the line beside the promotion's own
// fields. It mirrors the hosted ledger's column name, so a file line is
// still exactly what ImportLedger accepts.
const importedFromKey = "imported_from"

// ImportPromotionFrom records a promotion from another ledger verbatim —
// id, timestamp, actor and gates exactly as they were — and notes where the
// line came from.
//
// source is free-form provenance, and the shape the design names is
// "git:<path>@<blob sha>": the file AND the exact bytes, so the import can be
// re-derived from the repository rather than taken on trust. An empty source
// is refused, because a record that says "imported" without saying from
// where is strictly less useful than one that does not claim to be imported
// at all.
//
// IT PRESERVES THE ID, and that is a contract, not a convenience. The
// unimported-checkout refusal in internal/cli matches a checkout's promotion
// against the ledger BY ID, so an import that minted fresh ids would leave
// every record looking absent and the refusal would never go quiet. It is
// also what makes the import idempotent: a second run finds the id already
// present and appends nothing.
func (s *Store) ImportPromotionFrom(p release.Promotion, source string) error {
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("importing promotion %q of %s: a source is required "+
			"(the file and blob the line came from), because an imported record that cannot name its origin "+
			"cannot be re-derived", p.ID, p.Env)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.ID == "" {
		return fmt.Errorf("importing a promotion of %s: the source id is required, "+
			"because an import is identified by the record it came from", p.Env)
	}
	line, err := importedPromotionLine(p, source)
	if err != nil {
		return err
	}
	return s.withLock(func() error {
		history, err := s.promotionsLocked(p.Env)
		if err != nil {
			return err
		}
		// Idempotent on the SOURCE id, so a re-run of an interrupted
		// import neither duplicates a line nor refuses.
		for _, existing := range history {
			if existing.ID == p.ID {
				return nil
			}
		}
		return appendRecord(s.envPath("promotions", p.Env), line)
	})
}

// importedPromotionLine is the promotion's own canonical JSON with the
// provenance key added.
//
// Encoded through canonicalJSON and re-decoded into a map rather than
// composed from a wrapper struct, so the promotion's fields keep the exact
// names, order and omissions every other writer of this file produces. A
// parallel struct would be a second encoding of release.Promotion, free to
// drift from the first.
func importedPromotionLine(p release.Promotion, source string) (json.RawMessage, error) {
	encoded, err := canonicalJSON(p)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("encode imported promotion %q: %w", p.ID, err)
	}
	marker, err := canonicalJSON(source)
	if err != nil {
		return nil, err
	}
	fields[importedFromKey] = marker
	out, err := canonicalJSON(fields)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ImportedFrom is the source each of env's promotions was imported from,
// keyed by promotion id. A promotion this machine made itself is absent, not
// empty: "recorded here" and "imported from somewhere unnamed" are different
// facts and a caller must be able to tell them apart.
//
// Read as a raw map rather than through release.Promotion, because the key
// deliberately is not a field on that type.
func (s *Store) ImportedFrom(env string) (map[string]string, error) {
	out := map[string]string{}
	err := s.withLock(func() error {
		lines, err := readLines(s.envPath("promotions", env))
		if err != nil {
			return err
		}
		for i, raw := range lines {
			var line struct {
				ID           string `json:"id"`
				ImportedFrom string `json:"imported_from"`
			}
			if err := json.Unmarshal(raw, &line); err != nil {
				return fmt.Errorf("%s line %d: %w", s.envPath("promotions", env), i+1, err)
			}
			if line.ID != "" && line.ImportedFrom != "" {
				out[line.ID] = line.ImportedFrom
			}
		}
		return nil
	})
	return out, err
}
