package identity

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ImportFileVersion is the import file format ParseImportFile reads.
const ImportFileVersion = 1

// maxImportLine bounds one JSONL row.
const maxImportLine = 1 << 20

// importLine is one JSONL row, version 1:
//
//	{"v":1,"id":"4f1c…","email":"ada@acme.com","email_verified":true,
//	 "given_name":"Ada","family_name":"L","display_name":"Ada",
//	 "password_hash":"$2a$10$…",
//	 "links":[{"provider":"github","provider_user_id":"583231","provider_user_name":"ada"}],
//	 "metadata":{"legacy_plan":"pro"},"roles":["member"]}
//
// Unknown fields are refused, so a misspelt field, or a plaintext "password",
// is an error rather than silently dropped.
type importLine struct {
	V             int               `json:"v"`
	ID            string            `json:"id"`
	Email         string            `json:"email"`
	EmailVerified bool              `json:"email_verified"`
	GivenName     string            `json:"given_name"`
	FamilyName    string            `json:"family_name"`
	DisplayName   string            `json:"display_name"`
	PasswordHash  string            `json:"password_hash"`
	Links         []importLinkLine  `json:"links"`
	Metadata      map[string]string `json:"metadata"`
	Roles         []string          `json:"roles"`
}

type importLinkLine struct {
	Provider         string `json:"provider"`
	ProviderUserID   string `json:"provider_user_id"`
	ProviderUserName string `json:"provider_user_name"`
}

// ParseImportFile reads an import file and calls each with every row, in
// order, with its 1-based line number.
//
// Two encodings are read, told apart by the first byte:
//
//   - JSONL (the first non-blank byte is '{'): one JSON object per line, in
//     the version-1 shape above. Blank lines are skipped.
//   - CSV (anything else): a header row naming the columns, then one user per
//     row. Columns: id, email, email_verified (true/false/1/0/yes/no, empty is
//     false), given_name, family_name, display_name, password_hash, roles
//     (separated by spaces or ';'), one link.<provider> column per social
//     provider holding "<provider_user_id>" or
//     "<provider_user_id>:<provider_user_name>" (several separated by spaces),
//     one meta.<key> column per metadata key (an empty cell sets nothing), and
//     an optional v (1).
//
// A row that cannot be read is passed with its error (and whatever id it
// carried), and the parse continues; each returning an error stops it, and
// ParseImportFile returns that error. A file that cannot be read at all (a
// CSV header naming an unknown column) is an error before any row.
func ParseImportFile(r io.Reader, each func(line int, u ImportUser, err error) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		b, err := br.Peek(1)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n' {
			_, _ = br.ReadByte()
			continue
		}
		if b[0] == '{' {
			return parseImportJSONL(br, each)
		}
		return parseImportCSV(br, each)
	}
}

func parseImportJSONL(r io.Reader, each func(int, ImportUser, error) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxImportLine)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		u, err := decodeImportLine(raw)
		if cerr := each(line, u, err); cerr != nil {
			return cerr
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("line %d: longer than %d bytes", line+1, maxImportLine)
		}
		return err
	}
	return nil
}

func decodeImportLine(raw []byte) (ImportUser, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var l importLine
	if err := dec.Decode(&l); err != nil {
		// The id, when it can be read, so a report can name the row.
		var probe struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &probe)
		if strings.Contains(err.Error(), `unknown field "password"`) {
			return ImportUser{ID: probe.ID}, errors.New(`there is no plaintext "password" field: carry the hash as "password_hash"`)
		}
		return ImportUser{ID: probe.ID}, fmt.Errorf("not a version-%d import row: %w", ImportFileVersion, err)
	}
	if dec.More() {
		return ImportUser{ID: l.ID}, errors.New("more than one JSON value on the line")
	}
	if l.V != ImportFileVersion {
		return ImportUser{ID: l.ID}, fmt.Errorf("\"v\" is %d; this forge reads version %d", l.V, ImportFileVersion)
	}
	u := ImportUser{
		ID: l.ID, Email: l.Email, EmailVerified: l.EmailVerified,
		GivenName: l.GivenName, FamilyName: l.FamilyName, DisplayName: l.DisplayName,
		PasswordHash: l.PasswordHash, Metadata: l.Metadata, Roles: l.Roles,
	}
	for _, k := range l.Links {
		u.Links = append(u.Links, ImportLink(k))
	}
	return u, nil
}

// importCSVColumn is what one CSV header cell maps to.
type importCSVColumn struct {
	field string // a fixed column, or "link" / "meta"
	key   string // the provider or metadata key
}

var importCSVFields = map[string]bool{
	"v": true, "id": true, "email": true, "email_verified": true, "given_name": true,
	"family_name": true, "display_name": true, "password_hash": true, "roles": true,
}

func parseImportCSV(r io.Reader, each func(int, ImportUser, error) error) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the CSV header: %w", err)
	}
	cols := make([]importCSVColumn, len(header))
	seen := map[string]bool{}
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		switch {
		case importCSVFields[name]:
			cols[i] = importCSVColumn{field: name}
		case strings.HasPrefix(name, "link.") && len(name) > len("link."):
			cols[i] = importCSVColumn{field: "link", key: strings.TrimPrefix(name, "link.")}
		case strings.HasPrefix(strings.TrimSpace(h), "meta.") && len(strings.TrimSpace(h)) > len("meta."):
			// Metadata keys keep their case.
			cols[i] = importCSVColumn{field: "meta", key: strings.TrimPrefix(strings.TrimSpace(h), "meta.")}
		case name == "password":
			return errors.New(`CSV header: there is no plaintext "password" column; carry the hash as "password_hash"`)
		default:
			return fmt.Errorf("CSV header: unknown column %q (columns: id, email, email_verified, given_name, family_name, display_name, password_hash, roles, link.<provider>, meta.<key>, v)", h)
		}
		if seen[name] {
			return fmt.Errorf("CSV header: column %q appears twice", h)
		}
		seen[name] = true
	}
	if !seen["id"] || !seen["email"] {
		return errors.New("CSV header: the id and email columns are required")
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		// FieldPos is only valid for a record Read returned without error:
		// after a malformed row it can panic, so the error is handled first.
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				if cerr := each(pe.Line, ImportUser{}, fmt.Errorf("not a CSV row: %w", pe.Err)); cerr != nil {
					return cerr
				}
				continue
			}
			return err
		}
		line, _ := cr.FieldPos(0)
		u, rowErr := csvImportRow(cols, rec)
		if cerr := each(line, u, rowErr); cerr != nil {
			return cerr
		}
	}
}

func csvImportRow(cols []importCSVColumn, rec []string) (ImportUser, error) {
	var u ImportUser
	var errs []error
	if len(rec) > len(cols) {
		errs = append(errs, fmt.Errorf("%d cells for %d columns", len(rec), len(cols)))
	}
	for i, c := range cols {
		if i >= len(rec) {
			break
		}
		v := strings.TrimSpace(rec[i])
		switch c.field {
		case "v":
			if v != "" && v != "1" {
				errs = append(errs, fmt.Errorf("v is %q; this forge reads version %d", v, ImportFileVersion))
			}
		case "id":
			u.ID = v
		case "email":
			u.Email = v
		case "email_verified":
			switch strings.ToLower(v) {
			case "", "false", "0", "no", "n":
			case "true", "1", "yes", "y":
				u.EmailVerified = true
			default:
				errs = append(errs, fmt.Errorf("email_verified is %q, not true or false", v))
			}
		case "given_name":
			u.GivenName = v
		case "family_name":
			u.FamilyName = v
		case "display_name":
			u.DisplayName = v
		case "password_hash":
			u.PasswordHash = v
		case "roles":
			u.Roles = append(u.Roles, strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == ' ' || r == ',' })...)
		case "link":
			for _, l := range strings.Fields(v) {
				id, name, _ := strings.Cut(l, ":")
				u.Links = append(u.Links, ImportLink{Provider: c.key, ProviderUserID: id, ProviderUserName: name})
			}
		case "meta":
			if rec[i] != "" {
				if u.Metadata == nil {
					u.Metadata = map[string]string{}
				}
				u.Metadata[c.key] = rec[i]
			}
		}
	}
	return u, errors.Join(errs...)
}
