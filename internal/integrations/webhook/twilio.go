// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Twilio's webhook signature is HMAC-SHA1; the algorithm is Twilio's, not ours.
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Twilio Programmable Messaging: SMS, MMS and WhatsApp inbound messages.
//
// Twilio has no app-level webhook. Each phone number (or WhatsApp sender, or
// Messaging Service) has its own "A message comes in" URL, which the user
// sets in their Twilio console to POST <PUBLIC_URL>/integrations/twilio/events.
// Every user, every account and every number shares that one URL.
//
//   - Verify: X-Twilio-Signature is base64 HMAC-SHA1, under the account's
//     PRIMARY Auth Token, of the URL Twilio was configured with (scheme to
//     query string) followed by every POST parameter sorted by name, as
//     name+value with no delimiter (twilio.com/docs/usage/webhooks/
//     webhooks-security). Twilio signs that URL both with and without the
//     scheme's default port, so — as its own validators do — both are tried.
//     The URL is the receiver's PublicURL, rebuilt from PUBLIC_URL, never the
//     request's own Host: behind the ingress that is an internal address.
//   - The Auth Token is the account's, saved by its user as their Twilio
//     connection's credential: there is no deployment secret. So the provider
//     is ConnectionSigned, and the receiver verifies a delivery against the
//     token of each connection that records its AccountSid, and routes it
//     only through the connections whose token verified it.
//   - An API key secret never signs a webhook, which is why a Twilio
//     connection is Account SID + Auth Token (catalog/twilio/manifest.yaml).
//   - Replays: Twilio's signature has no timestamp. MessageSid is the dedupe
//     key, stable across Twilio's own retries, so a replayed delivery records
//     nothing new.
//   - Reply: Twilio reads the response body as TwiML. An empty <Response/>
//     tells it to send nothing back to the texter.

// TwilioProviderID is Twilio's integration id.
const TwilioProviderID = "twilio"

// twilioSignatureHeader carries the signature.
const twilioSignatureHeader = "X-Twilio-Signature"

// twilioMaxMedia bounds the media items read from one message (Twilio allows
// at most 10 on a send; a hostile NumMedia must not drive a loop).
const twilioMaxMedia = 20

// twilioEmptyTwiML is the reply that makes Twilio send nothing back.
var twilioEmptyTwiML = []byte(`<?xml version="1.0" encoding="UTF-8"?><Response></Response>`)

// TwilioProvider verifies and parses Twilio's inbound-message webhook.
type TwilioProvider struct{}

// NewTwilioProvider builds the provider. It holds no secret: each delivery is
// verified against the stored Auth Token of the connections it names.
func NewTwilioProvider() *TwilioProvider { return &TwilioProvider{} }

// ID implements Provider.
func (p *TwilioProvider) ID() string { return TwilioProviderID }

// UserConfiguresWebhook implements UserConfigured: each user points their
// own numbers at the events URL.
func (p *TwilioProvider) UserConfiguresWebhook() bool { return true }

// Verify implements Provider. A Twilio delivery can only be verified against
// a connection's Auth Token (VerifyWith), which the receiver supplies; called
// without one, it refuses.
func (p *TwilioProvider) Verify(context.Context, *Request) error {
	return fmt.Errorf("%w: a twilio delivery is verified per connection", ErrUnauthorized)
}

// SignedAccount implements ConnectionSigned: the AccountSid form field.
func (p *TwilioProvider) SignedAccount(req *Request) (string, error) {
	if req.Form == nil {
		return "", fmt.Errorf("%w: a twilio delivery is a form", ErrUnauthorized)
	}
	if strings.TrimSpace(req.Header.Get(twilioSignatureHeader)) == "" {
		return "", fmt.Errorf("%w: missing %s", ErrUnauthorized, twilioSignatureHeader)
	}
	account := req.Form.Get("AccountSid")
	if account == "" {
		return "", fmt.Errorf("%w: no AccountSid", ErrUnauthorized)
	}
	return account, nil
}

// VerifyWith implements ConnectionSigned. secret is a Twilio connection's
// stored basic credential, "<Account SID>\x00<Auth Token>"; the Auth Token is
// the HMAC key.
func (p *TwilioProvider) VerifyWith(req *Request, secret vault.Secret) error {
	if req.PublicURL == nil {
		return errors.New("twilio: no public URL to verify against")
	}
	if req.Form == nil {
		return fmt.Errorf("%w: a twilio delivery is a form", ErrUnauthorized)
	}
	got := strings.TrimSpace(req.Header.Get(twilioSignatureHeader))
	if got == "" {
		return fmt.Errorf("%w: missing %s", ErrUnauthorized, twilioSignatureHeader)
	}
	ok := false
	err := secret.Use(func(b []byte) error {
		token := b
		if i := bytes.IndexByte(b, 0); i >= 0 {
			token = b[i+1:]
		}
		if len(token) == 0 {
			return errors.New("twilio: the stored credential has no auth token")
		}
		for _, candidate := range twilioURLVariants(req.PublicURL) {
			want := twilioSignature(token, candidate, req.Form)
			if subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1 {
				ok = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: signature mismatch", ErrUnauthorized)
	}
	return nil
}

// twilioSignature is base64(HMAC-SHA1(token, url + sorted name+value pairs)).
// A repeated name contributes every value, in order.
func twilioSignature(token []byte, rawURL string, form url.Values) string {
	names := make([]string, 0, len(form))
	for name := range form {
		names = append(names, name)
	}
	sort.Strings(names)
	mac := hmac.New(sha1.New, token)
	mac.Write([]byte(rawURL))
	for _, name := range names {
		for _, v := range form[name] {
			mac.Write([]byte(name))
			mac.Write([]byte(v))
		}
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// twilioURLVariants is the public URL as configured, and the same URL with
// the scheme's default port added or removed: Twilio signs either form.
func twilioURLVariants(u *url.URL) []string {
	without, with := *u, *u
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	defaultPort := "443"
	if u.Scheme == "http" {
		defaultPort = "80"
	}
	switch u.Port() {
	case "":
		with.Host = net.JoinHostPort(u.Hostname(), defaultPort)
	case defaultPort:
		without.Host = host
	default:
		return []string{u.String()}
	}
	return []string{without.String(), with.String()}
}

// Parse implements Provider: one inbound message.
func (p *TwilioProvider) Parse(_ context.Context, req *Request) (*Delivery, error) {
	f := req.Form
	if f == nil {
		return nil, errors.New("twilio delivery is not a form")
	}
	sid := f.Get("MessageSid")
	if sid == "" {
		sid = f.Get("SmsMessageSid")
	}
	account := f.Get("AccountSid")
	if sid == "" || account == "" {
		return nil, errors.New("twilio delivery without MessageSid or AccountSid")
	}
	from, to := f.Get("From"), f.Get("To")
	channel := "sms"
	if strings.HasPrefix(from, "whatsapp:") || strings.HasPrefix(to, "whatsapp:") {
		channel = "whatsapp"
	}
	numMedia := atoiOrZero(f.Get("NumMedia"))
	media := make([]any, 0, min(numMedia, twilioMaxMedia))
	for i := 0; i < numMedia && i < twilioMaxMedia; i++ {
		u := f.Get("MediaUrl" + strconv.Itoa(i))
		if u == "" {
			continue
		}
		media = append(media, map[string]any{"url": u, "content_type": f.Get("MediaContentType" + strconv.Itoa(i))})
	}
	data := map[string]any{
		"message_sid": sid,
		"account_sid": account,
		"from":        from,
		"to":          to,
		"body":        f.Get("Body"),
		"channel":     channel,
		"num_media":   numMedia,
		"media":       media,
	}
	if n := f.Get("NumSegments"); n != "" {
		data["num_segments"] = atoiOrZero(n)
	}
	for field, key := range map[string]string{
		"MessagingServiceSid": "messaging_service_sid",
		"ProfileName":         "profile_name",
		"WaId":                "wa_id",
		"FromCity":            "from_city",
		"FromState":           "from_state",
		"FromCountry":         "from_country",
		"FromZip":             "from_zip",
		"ButtonText":          "button_text",
		"Latitude":            "latitude",
		"Longitude":           "longitude",
	} {
		if v := f.Get(field); v != "" {
			data[key] = v
		}
	}
	return &Delivery{
		Events: []Event{{
			Type:       "message.received",
			AccountKey: account,
			DeliveryID: sid,
			Attributes: map[string]string{
				"from": from, "to": to, "channel": channel, "num_media": strconv.Itoa(numMedia),
			},
			Data:   data,
			Sender: twilioSender(from, f.Get("ProfileName")),
		}},
		Ack: &Response{ContentType: "text/xml", Body: twilioEmptyTwiML},
	}, nil
}

// twilioSender is an inbound message's trigger.sender: its From number,
// exactly as Twilio sent it (E.164, or "whatsapp:+…").
//
// It is NOT verified, though the request's Twilio signature passed. The
// signature proves Twilio delivered the message, not who sent it: SMS has no
// end-to-end attestation of the originating number (STIR/SHAKEN covers voice
// calls only), a number can be spoofed upstream of Twilio, and Twilio says it
// cannot detect that when it happens outside its own platform
// (stackoverflow.com/q/53796583, answered by Twilio). WhatsApp's sender is
// attested by Meta rather than Twilio, which Twilio does not document as a
// guarantee, so it is treated the same. A filter can still read the number;
// an allowlist that requires a verified sender will not pass it.
func twilioSender(from, profileName string) *core.TriggerSender {
	return &core.TriggerSender{Kind: core.TriggerSenderKindSMS, ID: from, DisplayName: profileName, Verified: false}
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
