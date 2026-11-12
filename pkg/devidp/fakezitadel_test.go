package devidp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeZitadel is an in-memory stand-in for the parts of Zitadel's v2 API the
// sign-in routes drive. It is deliberately as PERMISSIVE as the real issuer
// where the real issuer is permissive — above all, it finalizes ANY session,
// verified factor or not, and it accepts any clientDataJSON origin — so the
// tests prove the broker's own checks, not the fake's.
type fakeZitadel struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	nextID   int
	users    map[string]*fakeUser
	sessions map[string]*fakeSession
	intents  map[string]*fakeIntent
	mails    []fakeMail // what the issuer "sent"
	calls    []string   // "METHOD /path", every call
	grants   []string   // "userID:role"

	// refusedPassword is a password the issuer's policy refuses although it
	// meets every rule the broker can see (DOMAIN-ZDLwA, "no symbol").
	refusedPassword string
	// passwordSetFails makes setting a password fail at the issuer (500).
	passwordSetFails bool
	// failLinks makes the link listing fail (a revocation that cannot
	// complete).
	failLinks bool
	// proofDelay holds the answer to a successful email or invitation
	// proof for this long AFTER the address is marked verified: the window
	// a concurrent sign-in races.
	proofDelay time.Duration
	// refusedProofDelay holds the answer to a REFUSED email proof (a
	// wrong code) for this long: a failing proof kept in flight.
	refusedProofDelay time.Duration
	// pageLimit caps every paged search answer at this many results, as
	// the real issuer's maximum does (0: the requested limit only).
	pageLimit int
	// failUnlock makes every unlock fail at the issuer (500).
	failUnlock bool
	// failMetadataDeletes makes that many metadata deletes fail (500).
	failMetadataDeletes int
	// beforeRequest, when set, runs before every request is served, with
	// mu released: a test pauses one request there to force an interleaving.
	beforeRequest func(*http.Request)
}

type fakeUser struct {
	id, email, loginName string
	verified             bool
	password             string
	otpEmail             bool
	passkeys             []Passkey
	pendingPasskey       string
	inviteCode           string
	emailCode            string
	pkRegCode            PasskeyRegistrationCode
	resetCode            string
	links                []IDPLink
	metadata             map[string]string
	locked               bool
	totp                 bool
}

type fakeSession struct {
	token    string
	userID   string
	verified []string
	otpCode  string
	webauthn bool
	deleted  bool
}

type fakeIntent struct {
	token  string
	result map[string]any
}

type fakeMail struct {
	userID, kind, link, code string
}

func newFakeZitadel(t *testing.T) *fakeZitadel {
	t.Helper()
	f := &fakeZitadel{t: t, users: map[string]*fakeUser{}, sessions: map[string]*fakeSession{}, intents: map[string]*fakeIntent{}}
	mux := http.NewServeMux()
	f.routes(mux)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		delay, refusedDelay, hook := f.proofDelay, f.refusedProofDelay, f.beforeRequest
		f.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		rec := &statusRecorder{ResponseWriter: w}
		mux.ServeHTTP(rec, r)
		// The answer is buffered until the handler returns, so the state
		// change is visible to other requests during the sleep.
		isProof := strings.HasSuffix(r.URL.Path, "/email/verify") || strings.HasSuffix(r.URL.Path, "/invite_code/verify")
		if isProof && rec.status < 300 && delay > 0 {
			time.Sleep(delay)
		}
		if isProof && rec.status >= 400 && refusedDelay > 0 {
			time.Sleep(refusedDelay)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeZitadel) client() *Client { return &Client{Base: f.srv.URL, Token: "broker-token"} }

// statusRecorder notes the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// userID is an id shaped like Zitadel's: 18 digits.
func (f *fakeZitadel) userID() string {
	f.nextID++
	return fmt.Sprintf("3%017d", f.nextID)
}

func (f *fakeZitadel) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

// addUser seeds an account.
func (f *fakeZitadel) addUser(email string, verified bool, password string) *fakeUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := &fakeUser{id: f.userID(), email: email, loginName: email, verified: verified, password: password}
	f.users[u.id] = u
	return u
}

func (f *fakeZitadel) user(id string) *fakeUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[id]
}

func (f *fakeZitadel) userByEmail(email string) *fakeUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.email, email) {
			return u
		}
	}
	return nil
}

// callCount counts calls whose "METHOD /path" starts with prefix.
func (f *fakeZitadel) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeZitadel) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeZitadel) lastMail(t *testing.T) fakeMail {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.mails) == 0 {
		t.Fatal("the issuer sent no mail")
	}
	return f.mails[len(f.mails)-1]
}

func (f *fakeZitadel) mailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mails)
}

// addIntent seeds a completed social intent.
func (f *fakeZitadel) addIntent(id, token string, result map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intents[id] = &fakeIntent{token: token, result: result}
}

func zerr(w http.ResponseWriter, status int, id, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 3, "message": msg + " (" + id + ")",
		"details": []any{map[string]any{"id": id, "message": msg}}})
}

func ok(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// page answers one page of a paged search the way the issuer does: the
// request's {offset, limit} query (capped at pageLimit) picks the results,
// and details.totalResult (a protojson uint64: a string) counts them all.
func (f *fakeZitadel) page(b map[string]any, resultKey string, all []any) map[string]any {
	q := sub(b, "query")
	offset := 0
	switch v := q["offset"].(type) {
	case string:
		offset, _ = strconv.Atoi(v)
	case float64:
		offset = int(v)
	}
	limit := len(all)
	if v, ok := q["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if f.pageLimit > 0 && limit > f.pageLimit {
		limit = f.pageLimit
	}
	res := []any{}
	if offset < len(all) {
		res = all[offset:min(offset+limit, len(all))]
	}
	return map[string]any{resultKey: res, "details": map[string]any{"totalResult": strconv.Itoa(len(all))}}
}

func readBody(r *http.Request) map[string]any {
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	return m
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, _ := m[k].(map[string]any)
		m = next
		if m == nil {
			return nil
		}
	}
	return m
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func (f *fakeZitadel) render(tmpl string, u *fakeUser, code, sessionID string) string {
	r := strings.NewReplacer("{{.Code}}", code, "{{.UserID}}", u.id, "{{.SessionID}}", sessionID, "{{.OrgID}}", "org1")
	return r.Replace(tmpl)
}

func humanJSON(u *fakeUser) map[string]any {
	state := "USER_STATE_ACTIVE"
	if u.locked {
		state = "USER_STATE_LOCKED"
	}
	return map[string]any{"userId": u.id, "preferredLoginName": u.loginName, "state": state,
		"human": map[string]any{"email": map[string]any{"email": u.email, "isVerified": u.verified}}}
}

func (f *fakeZitadel) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/v2/authorize", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://app.test/auth/sign-in?authRequest=V2_"+r.URL.Query().Get("code_challenge")[:6], http.StatusFound)
	})
	// THE REAL ISSUER'S BYPASS, reproduced: any session finalizes.
	mux.HandleFunc("POST /v2/oidc/auth_requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		s := sub(b, "session")
		f.mu.Lock()
		sess := f.sessions[str(s, "sessionId")]
		f.mu.Unlock()
		if sess == nil || sess.token != str(s, "sessionToken") || sess.deleted {
			zerr(w, 404, "SESSION-x", "session not found")
			return
		}
		ok(w, map[string]any{"callbackUrl": "http://app.test/cb?code=code-" + sess.userID})
	})
	mux.HandleFunc("POST /oauth/v2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		uid := strings.TrimPrefix(r.PostForm.Get("code"), "code-")
		claims, _ := json.Marshal(map[string]any{"sub": uid, "email": "x"})
		idt := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
		ok(w, map[string]any{"access_token": "at-" + uid, "id_token": idt, "expires_in": 3600})
	})
	mux.HandleFunc("GET /oidc/v1/userinfo", func(w http.ResponseWriter, r *http.Request) {
		uid := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
		if f.user(uid) == nil {
			w.WriteHeader(401)
			return
		}
		ok(w, map[string]any{"sub": uid})
	})

	// ── sessions ──
	mux.HandleFunc("POST /v2/sessions", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		uc := sub(b, "checks", "user")
		f.mu.Lock()
		defer f.mu.Unlock()
		var u *fakeUser
		for _, cand := range f.users {
			if cand.id == str(uc, "userId") || (str(uc, "loginName") != "" && cand.loginName == str(uc, "loginName")) {
				u = cand
			}
		}
		if u == nil {
			zerr(w, 404, "QUERY-Dfbg2", "User could not be found")
			return
		}
		sess := &fakeSession{token: f.id("st"), userID: u.id}
		if pw := sub(b, "checks", "password"); pw != nil {
			if u.locked {
				zerr(w, 400, "COMMAND-JLK35", "Errors.User.Locked")
				return
			}
			if str(pw, "password") != u.password {
				zerr(w, 400, "COMMAND-3M0fs", "Password is invalid")
				return
			}
			sess.verified = append(sess.verified, "password")
		}
		if in := sub(b, "checks", "idpIntent"); in != nil {
			it := f.intents[str(in, "idpIntentId")]
			if it == nil || it.token != str(in, "idpIntentToken") {
				zerr(w, 400, "COMMAND-intent", "intent invalid")
				return
			}
			sess.verified = append(sess.verified, "intent")
		}
		sid := f.id("s")
		resp := map[string]any{"sessionId": sid, "sessionToken": sess.token}
		ch := map[string]any{}
		if wa := sub(b, "challenges", "webAuthN"); wa != nil {
			if len(u.passkeys) == 0 {
				zerr(w, 400, "COMMAND-nopk", "no passkeys")
				return
			}
			sess.webauthn = true
			ch["webAuthN"] = map[string]any{"publicKeyCredentialRequestOptions": map[string]any{"publicKey": map[string]any{
				"challenge": "Y2hhbGxlbmdl", "rpId": str(wa, "domain"), "userVerification": "required",
				"allowCredentials": []any{map[string]any{"type": "public-key", "id": "a2V5"}}, "timeout": 300000}}}
		}
		if oe := sub(b, "challenges", "otpEmail"); oe != nil {
			if !u.otpEmail {
				zerr(w, 400, "COMMAND-otp", "otp email not set up")
				return
			}
			sess.otpCode = f.id("otp")
			if sc := sub(oe, "sendCode"); sc != nil {
				f.mails = append(f.mails, fakeMail{userID: u.id, kind: "otp", code: sess.otpCode,
					link: f.render(str(sc, "urlTemplate"), u, sess.otpCode, sid)})
			} else {
				ch["otpEmail"] = sess.otpCode
			}
		}
		if len(ch) > 0 {
			resp["challenges"] = ch
		}
		f.sessions[sid] = sess
		ok(w, resp)
	})
	mux.HandleFunc("PATCH /v2/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		sess := f.sessions[r.PathValue("id")]
		if sess == nil || sess.token != str(b, "sessionToken") {
			zerr(w, 404, "SESSION-nf", "session not found")
			return
		}
		if wa := sub(b, "checks", "webAuthN"); wa != nil {
			// The real issuer verifies signature and challenge but NOT the
			// origin; the fake stands in with a marker signature.
			cred := sub(wa, "credentialAssertionData", "response")
			if !sess.webauthn || str(cred, "signature") != "good" {
				zerr(w, 400, "COMMAND-wa", "webauthn invalid")
				return
			}
			sess.verified = append(sess.verified, "webAuthN")
		}
		if oe := sub(b, "checks", "otpEmail"); oe != nil {
			if sess.otpCode == "" || str(oe, "code") != sess.otpCode {
				zerr(w, 400, "COMMAND-otpinv", "code invalid")
				return
			}
			sess.otpCode = "" // single use
			sess.verified = append(sess.verified, "otpEmail")
		}
		sess.token = f.id("st")
		ok(w, map[string]any{"sessionToken": sess.token})
	})
	mux.HandleFunc("GET /v2/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sess := f.sessions[r.PathValue("id")]
		if sess == nil || sess.token != r.URL.Query().Get("sessionToken") {
			zerr(w, 404, "SESSION-nf", "session not found")
			return
		}
		factors := map[string]any{"user": map[string]any{"id": sess.userID, "verifiedAt": "2026-01-01T00:00:00Z"}}
		for _, v := range sess.verified {
			factors[v] = map[string]any{"verifiedAt": "2026-01-01T00:00:00Z"}
		}
		ok(w, map[string]any{"session": map[string]any{"id": r.PathValue("id"), "factors": factors}})
	})
	mux.HandleFunc("DELETE /v2/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if s := f.sessions[r.PathValue("id")]; s != nil {
			s.deleted = true
		}
		ok(w, map[string]any{})
	})

	// ── users ──
	mux.HandleFunc("POST /v2/users", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		var email string
		if qs, _ := b["queries"].([]any); len(qs) > 0 {
			q, _ := qs[0].(map[string]any)
			email = str(sub(q, "emailQuery"), "emailAddress")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		res := []any{}
		for _, u := range f.users {
			if email != "" && strings.EqualFold(u.email, email) {
				res = append(res, humanJSON(u))
			}
		}
		ok(w, map[string]any{"result": res})
	})
	mux.HandleFunc("GET /v2/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		u := f.user(r.PathValue("id"))
		if u == nil {
			zerr(w, 404, "QUERY-nf", "not found")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		ok(w, map[string]any{"user": humanJSON(u)})
	})
	mux.HandleFunc("POST /v2/users/human", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		em := sub(b, "email")
		pw := str(sub(b, "password"), "password")
		if pw != "" && len(pw) < 8 {
			zerr(w, 400, "DOMAIN-HuJf6", "Password is too short")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		u := &fakeUser{id: f.userID(), email: str(em, "email"), loginName: str(em, "email"), password: pw}
		u.verified, _ = em["isVerified"].(bool)
		if sc := sub(em, "sendCode"); sc != nil {
			u.emailCode = f.id("ev")
			f.mails = append(f.mails, fakeMail{userID: u.id, kind: "verify", code: u.emailCode, link: f.render(str(sc, "urlTemplate"), u, u.emailCode, "")})
		}
		if md, _ := b["metadata"].([]any); len(md) > 0 {
			u.metadata = map[string]string{}
			for _, e := range md {
				m, _ := e.(map[string]any)
				v, _ := base64.StdEncoding.DecodeString(str(m, "value"))
				u.metadata[str(m, "key")] = string(v)
			}
		}
		if links, _ := b["idpLinks"].([]any); len(links) > 0 {
			l, _ := links[0].(map[string]any)
			u.links = append(u.links, IDPLink{IdPID: str(l, "idpId"), UserID: str(l, "userId"), UserName: str(l, "userName")})
		}
		f.users[u.id] = u
		ok(w, map[string]any{"userId": u.id})
	})
	withUser := func(h func(w http.ResponseWriter, r *http.Request, u *fakeUser, b map[string]any)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b := readBody(r)
			f.mu.Lock()
			defer f.mu.Unlock()
			u := f.users[r.PathValue("id")]
			if u == nil {
				zerr(w, 404, "QUERY-nf", "not found")
				return
			}
			h(w, r, u, b)
		}
	}
	mux.HandleFunc("POST /v2/users/{id}/otp_email", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		if !u.verified {
			zerr(w, 400, "COMMAND-KLJ2d", "Errors.User.MFA.OTP.NotReady")
			return
		}
		if u.otpEmail {
			zerr(w, 409, "COMMAND-MKL2s", "Errors.User.MFA.OTP.AlreadyReady")
			return
		}
		u.otpEmail = true
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/email/verify", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if u.emailCode == "" || str(b, "verificationCode") != u.emailCode {
			zerr(w, 400, "CODE-woT0xc", "Code is invalid")
			return
		}
		u.emailCode, u.verified = "", true
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/email/resend", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		u.emailCode = f.id("ev")
		f.mails = append(f.mails, fakeMail{userID: u.id, kind: "verify", code: u.emailCode, link: f.render(str(sub(b, "sendCode"), "urlTemplate"), u, u.emailCode, "")})
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/invite_code/verify", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if u.inviteCode == "" || str(b, "verificationCode") != u.inviteCode {
			zerr(w, 400, "CODE-woT0xc", "Code is invalid")
			return
		}
		u.inviteCode, u.verified = "", true
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/password_reset", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		u.resetCode = f.id("rc")
		ok(w, map[string]any{"verificationCode": u.resetCode})
	}))
	mux.HandleFunc("POST /v2/users/{id}/password", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if u.resetCode == "" || str(b, "verificationCode") != u.resetCode {
			zerr(w, 400, "CODE-woT0xc", "Code is invalid")
			return
		}
		pw := str(sub(b, "newPassword"), "password")
		if len(pw) < 8 {
			zerr(w, 400, "DOMAIN-HuJf6", "Password is too short")
			return
		}
		if f.refusedPassword != "" && pw == f.refusedPassword {
			zerr(w, 400, "DOMAIN-ZDLwA", "Password must contain a symbol")
			return
		}
		if f.passwordSetFails {
			zerr(w, 500, "INTERNAL", "the issuer is down")
			return
		}
		u.password, u.resetCode = pw, ""
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("GET /v2/users/{id}/authentication_methods", func(w http.ResponseWriter, r *http.Request) {
		u := f.user(r.PathValue("id"))
		types := []string{}
		if u != nil && u.password != "" {
			types = append(types, "AUTHENTICATION_METHOD_TYPE_PASSWORD")
		}
		ok(w, map[string]any{"authMethodTypes": types})
	})
	mux.HandleFunc("POST /v2/users/{id}/passkeys/registration_link", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		u.pkRegCode = PasskeyRegistrationCode{ID: f.id("pc"), Code: f.id("pcs")}
		ok(w, map[string]any{"code": map[string]any{"id": u.pkRegCode.ID, "code": u.pkRegCode.Code}})
	}))
	mux.HandleFunc("POST /v2/users/{id}/passkeys/_search", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		res := []any{}
		for _, p := range u.passkeys {
			res = append(res, map[string]any{"id": p.ID, "name": p.Name, "state": "AUTH_FACTOR_STATE_READY"})
		}
		ok(w, map[string]any{"result": res, "details": map[string]any{"totalResult": strconv.Itoa(len(res))}})
	}))
	mux.HandleFunc("POST /v2/users/{id}/passkeys", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if c := sub(b, "code"); c != nil {
			if str(c, "id") != u.pkRegCode.ID || str(c, "code") != u.pkRegCode.Code || u.pkRegCode.ID == "" {
				zerr(w, 400, "CODE-pk", "registration code invalid")
				return
			}
		}
		u.pendingPasskey = f.id("pk")
		ok(w, map[string]any{"passkeyId": u.pendingPasskey, "publicKeyCredentialCreationOptions": map[string]any{
			"publicKey": map[string]any{"challenge": "Y2g", "rp": map[string]any{"id": str(b, "domain"), "name": "app"},
				"user": map[string]any{"id": "dXNlcg", "name": u.email, "displayName": u.email}}}})
	}))
	mux.HandleFunc("POST /v2/users/{id}/passkeys/{pid}", withUser(func(w http.ResponseWriter, r *http.Request, u *fakeUser, b map[string]any) {
		if r.PathValue("pid") != u.pendingPasskey {
			zerr(w, 404, "COMMAND-pknf", "passkey not found")
			return
		}
		u.passkeys = append(u.passkeys, Passkey{ID: u.pendingPasskey, Name: str(b, "passkeyName")})
		u.pendingPasskey = ""
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/links", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		l := sub(b, "idpLink")
		u.links = append(u.links, IDPLink{IdPID: str(l, "idpId"), UserID: str(l, "userId"), UserName: str(l, "userName")})
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/links/_search", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if f.failLinks {
			zerr(w, 503, "INTERNAL-x", "unavailable")
			return
		}
		res := []any{}
		for _, l := range u.links {
			res = append(res, map[string]any{"idpId": l.IdPID, "userId": l.UserID, "userName": l.UserName})
		}
		ok(w, f.page(b, "result", res))
	}))
	mux.HandleFunc("DELETE /v2/users/{id}/links/{idp}/{linked}", withUser(func(w http.ResponseWriter, r *http.Request, u *fakeUser, _ map[string]any) {
		kept := u.links[:0]
		for _, l := range u.links {
			if l.IdPID != r.PathValue("idp") || l.UserID != r.PathValue("linked") {
				kept = append(kept, l)
			}
		}
		u.links = kept
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("DELETE /v2/users/{id}/passkeys/{pid}", withUser(func(w http.ResponseWriter, r *http.Request, u *fakeUser, _ map[string]any) {
		kept := u.passkeys[:0]
		for _, p := range u.passkeys {
			if p.ID != r.PathValue("pid") {
				kept = append(kept, p)
			}
		}
		u.passkeys = kept
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/unlock", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		if f.failUnlock {
			zerr(w, 500, "COMMAND-fake", "unlock failed")
			return
		}
		if !u.locked {
			zerr(w, 400, "COMMAND-olb9vb0oca", "Errors.User.NotLocked")
			return
		}
		u.locked = false
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("DELETE /v2/users/{id}/totp", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		if !u.totp {
			zerr(w, 404, "COMMAND-Hd9sd", "Errors.User.MFA.OTP.NotExisting")
			return
		}
		u.totp = false
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("DELETE /v2/users/{id}/otp_sms", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		zerr(w, 404, "COMMAND-Sr3h3", "Errors.User.MFA.OTP.NotExisting")
	}))
	mux.HandleFunc("POST /v2/sessions/search", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		var uid string
		if qs, _ := b["queries"].([]any); len(qs) > 0 {
			q, _ := qs[0].(map[string]any)
			uid = str(sub(q, "userIdQuery"), "id")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		ids := []string{}
		for id, sess := range f.sessions {
			if sess.userID == uid && !sess.deleted {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids) // a stable order, so offsets mean the same on every page
		res := []any{}
		for _, id := range ids {
			res = append(res, map[string]any{"id": id})
		}
		ok(w, f.page(b, "sessions", res))
	})
	mux.HandleFunc("POST /v2/users/{id}/lock", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, _ map[string]any) {
		if u.locked {
			zerr(w, 400, "COMMAND-lgws8wtsqf", "Errors.User.ShouldBeActiveOrInitial")
			return
		}
		u.locked = true
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/metadata/search", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		want, prefix := "", false
		if fs, _ := b["filters"].([]any); len(fs) > 0 {
			f, _ := fs[0].(map[string]any)
			want = str(sub(f, "keyFilter"), "key")
			prefix = str(sub(f, "keyFilter"), "method") == "TEXT_FILTER_METHOD_STARTS_WITH"
		}
		res := []any{}
		for k, v := range u.metadata {
			if want == "" || k == want || (prefix && strings.HasPrefix(k, want)) {
				res = append(res, map[string]any{"key": k, "value": base64.StdEncoding.EncodeToString([]byte(v))})
			}
		}
		ok(w, map[string]any{"metadata": res})
	}))
	mux.HandleFunc("DELETE /v2/users/{id}/metadata", withUser(func(w http.ResponseWriter, r *http.Request, u *fakeUser, _ map[string]any) {
		if f.failMetadataDeletes > 0 {
			f.failMetadataDeletes--
			zerr(w, 500, "COMMAND-fake", "metadata delete failed")
			return
		}
		for _, k := range r.URL.Query()["keys"] {
			delete(u.metadata, k)
		}
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /v2/users/{id}/metadata", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		if u.metadata == nil {
			u.metadata = map[string]string{}
		}
		md, _ := b["metadata"].([]any)
		for _, e := range md {
			m, _ := e.(map[string]any)
			v, _ := base64.StdEncoding.DecodeString(str(m, "value"))
			u.metadata[str(m, "key")] = string(v)
		}
		ok(w, map[string]any{})
	}))
	mux.HandleFunc("POST /management/v1/users/{id}/grants", withUser(func(w http.ResponseWriter, _ *http.Request, u *fakeUser, b map[string]any) {
		roles, _ := b["roleKeys"].([]any)
		for _, role := range roles {
			f.grants = append(f.grants, u.id+":"+fmt.Sprint(role))
		}
		ok(w, map[string]any{})
	}))

	// ── social ──
	mux.HandleFunc("POST /v2/idp_intents", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		f.mu.Lock()
		id := f.id("intent")
		f.mu.Unlock()
		ok(w, map[string]any{"authUrl": "https://accounts.example/authorize?client_id=x&state=" + id + "&idp=" + str(b, "idpId")})
	})
	mux.HandleFunc("POST /v2/idp_intents/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		it := f.intents[r.PathValue("id")]
		if it == nil || it.token != str(b, "idpIntentToken") {
			zerr(w, 400, "IDP-intent", "intent invalid")
			return
		}
		res := map[string]any{}
		for k, v := range it.result {
			res[k] = v
		}
		// The issuer resolves an already-linked identity to its account.
		info := sub(it.result, "idpInformation")
		for _, u := range f.users {
			for _, l := range u.links {
				if l.IdPID == str(info, "idpId") && l.UserID == str(info, "userId") {
					res["userId"] = u.id
				}
			}
		}
		ok(w, res)
	})
}
