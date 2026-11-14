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
// mail providers: SMTP, HTTP, none
// ---------------------------------------------------------------------------

// emailProviderObj is one of the instance's email providers as the
// /admin/v1/email API reads it: an SMTP relay or an HTTP endpoint. The legacy
// /admin/v1/smtp search lists HTTP providers too but cannot tell them apart
// from SMTP ones, and omits the reply-to address, so drift in it could never
// be read; this API answers both.
type emailProviderObj struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Description string `json:"description"`
	SMTP        *struct {
		SenderAddress string `json:"senderAddress"`
		SenderName    string `json:"senderName"`
		TLS           bool   `json:"tls"`
		Host          string `json:"host"`
		User          string `json:"user"`
		ReplyTo       string `json:"replyToAddress"`
	} `json:"smtp"`
	HTTP *struct {
		Endpoint   string `json:"endpoint"`
		SigningKey string `json:"signingKey"`
	} `json:"http"`
}

func (e emailProviderObj) active() bool { return e.State == "EMAIL_PROVIDER_ACTIVE" }

// mailDescription marks the providers this library manages.
const mailDescription = "managed by forge identity"

// ensureMail converges the instance's one active email provider to the
// declared one (Spec.SMTP, Spec.EmailHTTP or Spec.EmailNone). Activation is
// exclusive in Zitadel: activating one provider deactivates the one that was
// active, so switching between SMTP and HTTP needs no separate deactivation.
func (p *run) ensureMail(ctx context.Context) error {
	if p.spec.SMTP == nil && p.spec.EmailHTTP == nil && !p.spec.EmailNone {
		return nil
	}
	all, err := pageAll[emailProviderObj](ctx, p.c, "/admin/v1/email/_search", map[string]any{}, noOrg)
	if err != nil {
		return objErr("mail provider", "instance", err)
	}
	switch {
	case p.spec.SMTP != nil:
		return p.ensureSMTP(ctx, all)
	case p.spec.EmailHTTP != nil:
		return p.ensureHTTPEmail(ctx, all)
	default:
		return p.ensureNoEmail(ctx, all)
	}
}

func (p *run) activateEmail(ctx context.Context, id, name string) error {
	err := p.c.call(ctx, http.MethodPost, "/admin/v1/email/"+url.PathEscape(id)+"/_activate", map[string]any{}, nil, idem)
	if err != nil && !isNoChange(err) {
		return objErr("mail provider", name, fmt.Errorf("activate: %w", err))
	}
	return nil
}

// ensureHTTPEmail converges the managed HTTP email provider.
//
// Zitadel generates the provider's signing key and returns it at creation;
// the receiving endpoint verifies every notification with it, so it is kept in
// the KeyStore (KeyMailSigning). A store that lost it is repaired from the
// provider list, which serves the key decrypted, and failing that the key is
// rotated in place (the provider keeps its id): Zitadel signs with the new key
// at once, and the endpoint reads it from the store on its next request.
func (p *run) ensureHTTPEmail(ctx context.Context, all []emailProviderObj) error {
	want := p.spec.EmailHTTP
	var cur *emailProviderObj
	for i := range all {
		if all[i].Description == mailDescription && all[i].HTTP != nil {
			cur = &all[i]
		}
	}
	stored, err := p.o.Keys.Load(ctx, KeyMailSigning)
	if err != nil {
		return objErr("mail provider", want.Endpoint, fmt.Errorf("read the stored signing key: %w", err))
	}
	if cur == nil {
		p.rep.change(Created, "mail provider", want.Endpoint, "http")
		if p.dry() {
			return nil
		}
		var out struct {
			ID         string `json:"id"`
			SigningKey string `json:"signingKey"`
		}
		if err := p.c.call(ctx, http.MethodPost, "/admin/v1/email/http", map[string]any{
			"endpoint": want.Endpoint, "description": mailDescription,
		}, &out, mint); err != nil {
			return objErr("mail provider", want.Endpoint, err)
		}
		if err := p.keepSigningKey(ctx, want.Endpoint, out.SigningKey); err != nil {
			return err
		}
		return p.activateEmail(ctx, out.ID, want.Endpoint)
	}

	base := "/admin/v1/email/http/" + url.PathEscape(cur.ID)
	if cur.HTTP.Endpoint != want.Endpoint {
		p.rep.change(Updated, "mail provider", want.Endpoint, "endpoint")
		if !p.dry() {
			err := p.c.call(ctx, http.MethodPut, base, map[string]any{"endpoint": want.Endpoint, "description": mailDescription}, nil, idem)
			if err != nil && !isNoChange(err) {
				return objErr("mail provider", want.Endpoint, err)
			}
		}
	}
	switch {
	case cur.HTTP.SigningKey != "" && string(stored) == cur.HTTP.SigningKey:
	case cur.HTTP.SigningKey != "":
		// The store lost (or never got) the key; the instance still has it.
		if !p.dry() {
			if err := p.keepSigningKey(ctx, want.Endpoint, cur.HTTP.SigningKey); err != nil {
				return err
			}
		}
	case len(stored) > 0:
		// The instance does not show the key, but the store has it: keep it.
		// Rotating here would rewrite a working key on every pass.
	default:
		p.rep.change(Updated, "mail provider", want.Endpoint, "signing key rotated")
		if !p.dry() {
			var out struct {
				SigningKey string `json:"signingKey"`
			}
			if err := p.c.call(ctx, http.MethodPut, base, map[string]any{
				"endpoint": want.Endpoint, "description": mailDescription, "expirationSigningKey": "0s",
			}, &out, mint); err != nil {
				return objErr("mail provider", want.Endpoint, fmt.Errorf("rotate the signing key: %w", err))
			}
			if err := p.keepSigningKey(ctx, want.Endpoint, out.SigningKey); err != nil {
				return err
			}
		}
	}
	if !cur.active() {
		p.rep.change(Updated, "mail provider", want.Endpoint, "activation")
		if !p.dry() {
			return p.activateEmail(ctx, cur.ID, want.Endpoint)
		}
	}
	return nil
}

func (p *run) keepSigningKey(ctx context.Context, name, key string) error {
	if key == "" {
		return objErr("mail provider", name, fmt.Errorf("the instance returned no signing key"))
	}
	if err := p.o.Keys.Save(ctx, KeyMailSigning, []byte(key)); err != nil {
		return objErr("mail provider", name, fmt.Errorf("record the signing key: %w", err))
	}
	return nil
}

// ensureNoEmail deactivates the managed provider, if it is active. A provider
// this library did not make is not its to switch off.
func (p *run) ensureNoEmail(ctx context.Context, all []emailProviderObj) error {
	for _, e := range all {
		if e.Description != mailDescription || !e.active() {
			continue
		}
		p.rep.change(Updated, "mail provider", e.ID, "deactivated")
		if p.dry() {
			continue
		}
		err := p.c.call(ctx, http.MethodPost, "/admin/v1/email/"+url.PathEscape(e.ID)+"/_deactivate", map[string]any{}, nil, idem)
		if err != nil && !isNoChange(err) {
			return objErr("mail provider", e.ID, fmt.Errorf("deactivate: %w", err))
		}
	}
	return nil
}

// ensureSMTP converges the instance's one active SMTP configuration.
//
// Zitadel never returns the password, so drift in it cannot be read. A digest
// of the last password applied is therefore kept in the KeyStore: a changed
// password is detected against that digest, and an unchanged one costs no
// write. The digest is not the password and is useless without the declared
// value.
func (p *run) ensureSMTP(ctx context.Context, providers []emailProviderObj) error {
	want := p.spec.SMTP
	type smtpObj struct {
		ID, SenderAddress, SenderName, Host, User, State, Description, ReplyTo string
		TLS                                                                    bool
	}
	var all []smtpObj
	for _, e := range providers {
		if e.SMTP == nil {
			continue
		}
		state := "SMTP_CONFIG_INACTIVE"
		if e.active() {
			state = "SMTP_CONFIG_ACTIVE"
		}
		all = append(all, smtpObj{ID: e.ID, SenderAddress: e.SMTP.SenderAddress, SenderName: e.SMTP.SenderName,
			Host: e.SMTP.Host, User: e.SMTP.User, State: state, Description: e.Description, ReplyTo: e.SMTP.ReplyTo, TLS: e.SMTP.TLS})
	}
	var cur *smtpObj
	for i := range all {
		if all[i].Description == mailDescription {
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
		"description":    mailDescription,
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
		cur.TLS != want.TLS || cur.Host != want.Host || cur.User != want.User || cur.ReplyTo != want.ReplyTo || cur.Description != mailDescription {
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
	if len(p.idpIDs) > 0 {
		p.rep.Output.IdPIDs = p.idpIDs
	}
	if si := p.spec.SignIn; si != nil {
		doc, err := p.loginPolicyDoc(ctx, *si)
		if err != nil {
			return err
		}
		p.rep.Output.LoginPolicy = doc
	}
	if p.dry() {
		p.rep.Output.JWKS = nil
	}
	return nil
}

// socialLabels are the button labels of the managed IdP types.
var socialLabels = map[IdPType]string{IdPGoogle: "Google", IdPGitHub: "GitHub"}

// loginPolicyDoc builds the LOGIN_POLICY document from the declaration and
// what this pass converged: the IdPs' ids exist only after ensureIdPs, and the
// password rules are the instance's as converged (or as found, when the spec
// leaves complexity alone).
func (p *run) loginPolicyDoc(ctx context.Context, si SignIn) (*LoginPolicyDoc, error) {
	doc := &LoginPolicyDoc{
		Version:       LoginPolicyVersion,
		Signup:        string(si.mode()),
		SignupDomains: append([]string{}, si.SignupDomains...),
		SignupRole:    si.SignupRole,
		PasskeyRPID:   si.PasskeyRPID,
		Methods:       []string{},
		Social:        []LoginPolicySocial{},
	}
	for _, m := range si.Methods {
		doc.Methods = append(doc.Methods, string(m))
	}
	for _, i := range p.spec.IdPs {
		doc.Social = append(doc.Social, LoginPolicySocial{Type: string(i.Type), IdPID: p.idpIDs[i.Name], Label: socialLabels[i.Type]})
	}
	if pw := p.spec.Policies.PasswordComplexity; pw != nil {
		doc.Password = &LoginPolicyPassword{MinLength: pw.MinLength, Uppercase: pw.RequireUppercase,
			Lowercase: pw.RequireLowercase, Number: pw.RequireNumber, Symbol: pw.RequireSymbol}
		return doc, nil
	}
	cur, err := p.getPolicy(ctx, "/admin/v1/policies/password/complexity")
	if err != nil {
		return nil, objErr("password complexity policy", "instance", err)
	}
	flag := func(k string) bool { b, _ := cur[k].(bool); return b }
	doc.Password = &LoginPolicyPassword{MinLength: int(num(cur["minLength"])), Uppercase: flag("hasUppercase"),
		Lowercase: flag("hasLowercase"), Number: flag("hasNumber"), Symbol: flag("hasSymbol")}
	return doc, nil
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
