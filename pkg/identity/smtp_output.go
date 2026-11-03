package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------------------
// SMTP
// ---------------------------------------------------------------------------

type smtpObj struct {
	ID            string `json:"id"`
	SenderAddress string `json:"senderAddress"`
	SenderName    string `json:"senderName"`
	TLS           bool   `json:"tls"`
	Host          string `json:"host"`
	User          string `json:"user"`
	State         string `json:"state"`
	Description   string `json:"description"`
	ReplyTo       string `json:"replyToAddress"`
}

const smtpDescription = "managed by forge identity"

// ensureSMTP converges the instance's one active SMTP configuration.
//
// Zitadel never returns the password, so drift in it cannot be read. A digest
// of the last password applied is therefore kept in the KeyStore: a changed
// password is detected against that digest, and an unchanged one costs no
// write. The digest is not the password and is useless without the declared
// value.
func (p *run) ensureSMTP(ctx context.Context) error {
	want := p.spec.SMTP
	if want == nil {
		return nil
	}
	all, err := pageAll[smtpObj](ctx, p.c, "/admin/v1/smtp/_search", map[string]any{}, noOrg)
	if err != nil {
		return objErr("smtp", want.Host, err)
	}
	var cur *smtpObj
	for i := range all {
		if all[i].Description == smtpDescription {
			cur = &all[i]
		}
	}
	if cur == nil {
		// Adoption: an active configuration someone else made is taken over
		// rather than shadowed by a second one.
		for i := range all {
			if all[i].State == "SMTP_CONFIG_ACTIVE" {
				cur = &all[i]
			}
		}
	}

	body := map[string]any{
		"senderAddress":  want.SenderAddress,
		"senderName":     firstNonEmpty(want.SenderName, "Reliant"),
		"tls":            want.TLS,
		"host":           want.Host,
		"user":           want.User,
		"replyToAddress": want.ReplyTo,
		"description":    smtpDescription,
	}
	digest := passwordDigest(*want)
	storedDigest := ""
	if want.Password != "" {
		raw, err := p.o.Keys.Load(ctx, keySMTPDigest)
		if err != nil {
			return objErr("smtp", want.Host, fmt.Errorf("read the stored password digest: %w", err))
		}
		storedDigest = string(raw)
	}

	if cur == nil {
		p.rep.change(Created, "smtp", want.Host, "")
		if p.dry() {
			return nil
		}
		create := map[string]any{"password": want.Password}
		for k, v := range body {
			create[k] = v
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := p.c.call(ctx, http.MethodPost, "/admin/v1/smtp", create, &out, mint); err != nil {
			return objErr("smtp", want.Host, err)
		}
		if err := p.activateSMTP(ctx, out.ID, want.Host); err != nil {
			return err
		}
		return p.keepDigest(ctx, want, digest)
	}

	var drift []string
	if cur.SenderAddress != want.SenderAddress || cur.SenderName != body["senderName"] ||
		cur.TLS != want.TLS || cur.Host != want.Host || cur.User != want.User || cur.ReplyTo != want.ReplyTo || cur.Description != smtpDescription {
		drift = append(drift, "settings")
	}
	passwordChanged := want.Password != "" && storedDigest != digest
	if passwordChanged {
		drift = append(drift, "password")
	}
	if cur.State != "SMTP_CONFIG_ACTIVE" {
		drift = append(drift, "activation")
	}
	if len(drift) == 0 {
		return nil
	}
	p.rep.change(Updated, "smtp", want.Host, strings.Join(drift, ", "))
	if p.dry() {
		return nil
	}
	base := "/admin/v1/smtp/" + url.PathEscape(cur.ID)
	if contains(drift, "settings") {
		if err := p.c.call(ctx, http.MethodPut, base, body, nil, idem); err != nil && !isNoChange(err) {
			return objErr("smtp", want.Host, err)
		}
	}
	if passwordChanged {
		if err := p.c.call(ctx, http.MethodPut, base+"/password", map[string]any{"password": want.Password}, nil, idem); err != nil && !isNoChange(err) {
			return objErr("smtp", want.Host, fmt.Errorf("set the password: %w", err))
		}
		if err := p.keepDigest(ctx, want, digest); err != nil {
			return err
		}
	}
	if contains(drift, "activation") {
		return p.activateSMTP(ctx, cur.ID, want.Host)
	}
	return nil
}

func (p *run) activateSMTP(ctx context.Context, id, host string) error {
	err := p.c.call(ctx, http.MethodPost, "/admin/v1/smtp/"+url.PathEscape(id)+"/_activate", map[string]any{}, nil, idem)
	if err != nil && !isNoChange(err) {
		return objErr("smtp", host, fmt.Errorf("activate: %w", err))
	}
	return nil
}

func (p *run) keepDigest(ctx context.Context, want *SMTP, digest string) error {
	if want.Password == "" {
		return nil
	}
	if err := p.o.Keys.Save(ctx, keySMTPDigest, []byte(digest)); err != nil {
		return objErr("smtp", want.Host, fmt.Errorf("record the password digest: %w", err))
	}
	return nil
}

func passwordDigest(s SMTP) string {
	sum := sha256.Sum256([]byte("forge-identity-smtp\x00" + s.Host + "\x00" + s.User + "\x00" + s.Password))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// output
// ---------------------------------------------------------------------------

func (p *run) buildOutput(ctx context.Context) error {
	issuer, err := p.c.resolveIssuer(ctx)
	if err != nil {
		return err
	}
	jwks, err := p.fetchJWKS(ctx)
	if err != nil {
		return err
	}
	p.rep.Output = Output{
		Issuer:     issuer,
		OrgID:      p.c.orgID,
		ProjectID:  p.projectID,
		Audience:   p.projectID,
		RolesClaim: ProjectRolesClaim(p.projectID),
		Scopes:     []string{"openid", "profile", "email", RolesScope, AudienceScope(p.projectID)},
		JWKS:       jwks,
		Apps:       p.apps,
		Machine:    p.machine,
	}
	if p.dry() {
		p.rep.Output.JWKS = nil
	}
	return nil
}

// fetchJWKS snapshots the instance's public signing keys. A validator that must
// start while the issuer is unreachable falls back to the snapshot, so it is
// refreshed on every pass and therefore follows key rotation.
func (p *run) fetchJWKS(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := p.c.call(ctx, http.MethodGet, "/oauth/v2/keys", nil, &raw, anon); err != nil {
		return nil, objErr("jwks", p.c.base, err)
	}
	var probe struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Keys) == 0 {
		return nil, objErr("jwks", p.c.base, fmt.Errorf("the instance served no signing keys"))
	}
	return raw, nil
}
