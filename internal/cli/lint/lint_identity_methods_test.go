package lint

import (
	"strings"
	"testing"
)

const identityV2KCL = `import forge

# methods = ["passkey"]  (a comment declares nothing)
IDENTITY = forge.ManagedIdentity {
    signup = "open"
    methods = [
        "password", "passkey", "magic_link",
    ]
    social = [forge.IdentityGoogle {client_id = "x", client_secret = forge.ManagedSecret {name = "G"}}]
}
`

// A broker scaffolded before identity v2: no policy, no v2 routes.
const legacyBroker = `package app

const RegisterPath = "POST /auth/register"

func RegisterLoginBroker(mux *http.ServeMux) {
	mux.Handle(LoginPath, http.HandlerFunc(b.handleLogin))
	if broker.selfSignup {
		mux.Handle(RegisterPath, http.HandlerFunc(broker.handleRegister))
	}
}
`

const v2Broker = `package app

func RegisterLoginBroker(mux *http.ServeMux, cfg *config.Config) {
	policy, _ := devidp.ParseLoginPolicy(cfg.IdpLoginPolicy)
	if broker.selfSignup {
		mux.Handle(RegisterPath, http.HandlerFunc(broker.handleRegister))
	}
	if policy.Offers(devidp.MethodPasskey) {
		mux.Handle(PasskeyBeginPath, http.HandlerFunc(m.HandlePasskeyBegin))
	}
	mux.Handle(InviteAcceptPath, http.HandlerFunc(m.HandleInviteAccept))
	_ = "/auth/"
}
`

func TestIdentityMethods_LegacyBrokerIgnoresEveryDeclaration(t *testing.T) {
	root := t.TempDir()
	writeOpenSignupFile(t, root, "deploy/kcl/identity.k", identityV2KCL)
	writeOpenSignupFile(t, root, "internal/app/login_broker.go", legacyBroker)
	got, err := collectIdentityMethodsJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d findings, want 4 (passkey, magic_link, social, signup): %+v", len(got), got)
	}
	for _, f := range got {
		if f.Rule != ruleIdentityMethods || f.Severity != "error" || f.File != "deploy/kcl/identity.k" || f.Line == 0 {
			t.Errorf("finding %+v", f)
		}
		if !strings.Contains(f.Message, "idp_login_policy") || !strings.Contains(f.FixHint, "migrations/v1.1.0/identity-v2") {
			t.Errorf("finding does not name the cause and the fix: %+v", f)
		}
	}
}

func TestIdentityMethods_NamesOnlyTheRoutesAV2BrokerLacks(t *testing.T) {
	root := t.TempDir()
	writeOpenSignupFile(t, root, "deploy/kcl/identity.k", identityV2KCL)
	writeOpenSignupFile(t, root, "internal/app/login_broker.go", v2Broker)
	got, err := collectIdentityMethodsJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	var whats []string
	for _, f := range got {
		whats = append(whats, f.Message)
	}
	joined := strings.Join(whats, "\n")
	if len(got) != 2 || !strings.Contains(joined, "MagicRequestPath") || !strings.Contains(joined, "SocialStartPath") {
		t.Fatalf("want exactly the magic-link and social gaps, got:\n%s", joined)
	}
}

func TestIdentityMethods_QuietCases(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"default declaration": {
			"deploy/kcl/identity.k":        "import forge\nIDENTITY = forge.ManagedIdentity {signup = \"invite\", methods = [\"password\"]}\n",
			"internal/app/login_broker.go": legacyBroker,
		},
		"declared only in a comment": {
			"deploy/kcl/identity.k":        "# signup = \"open\"\n# social = [forge.IdentityGoogle {}]\n",
			"internal/app/login_broker.go": legacyBroker,
		},
		"no native broker": {"deploy/kcl/identity.k": identityV2KCL},
		"a full v2 broker (literal routes)": {
			"deploy/kcl/identity.k": identityV2KCL,
			"internal/app/login_broker.go": v2Broker + `
func more(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/magic/request", h)
	mux.HandleFunc("/auth/social/start", h)
}
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for rel, src := range files {
				writeOpenSignupFile(t, root, rel, src)
			}
			got, err := collectIdentityMethodsJSON(root)
			if err != nil || len(got) != 0 {
				t.Fatalf("got %+v, %v; want nothing", got, err)
			}
		})
	}
}
