package auth

import (
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Zitadel asserts roles as role -> {orgId: orgDomain}, namespaced by project.
// The shapes below are what a real v4.19.4 instance put in an access token
// (captured from a live instance while moving forge's dev pin).
const (
	zitProject      = "395169387633443593"
	zitProjectClaim = "urn:zitadel:iam:org:project:" + zitProject + ":roles"
	zitOtherClaim   = "urn:zitadel:iam:org:project:777000000000000001:roles"
	zitAggregate    = "urn:zitadel:iam:org:project:roles"
)

func zitadelRoles(roles map[string][]string) map[string]any {
	out := map[string]any{}
	for role, orgs := range roles {
		m := map[string]any{}
		for _, o := range orgs {
			m[o] = o + ".example"
		}
		out[role] = m
	}
	return out
}

func TestRoles_Matrix(t *testing.T) {
	tests := []struct {
		name     string
		claims   map[string]any
		opts     claimOpts
		want     []string
		wantRole string
	}{
		{
			name:   "generic flat roles array is unchanged",
			claims: map[string]any{"roles": []any{"viewer", "editor"}, "role": "editor"},
			want:   []string{"viewer", "editor"}, wantRole: "editor",
		},
		{
			name:   "no roles at all",
			claims: map[string]any{"sub": "u"},
			want:   nil,
		},
		{
			name: "zitadel single org, one role: project claim, Role is set too",
			claims: map[string]any{
				zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"org-1"}}),
				zitAggregate:    zitadelRoles(map[string][]string{"admin": {"org-1"}}),
			},
			opts: claimOpts{audience: zitProject},
			want: []string{"admin"}, wantRole: "admin",
		},
		{
			name: "zitadel single org, several roles: sorted, Role left empty",
			claims: map[string]any{
				zitProjectClaim: zitadelRoles(map[string][]string{"editor": {"org-1"}, "admin": {"org-1"}}),
			},
			opts: claimOpts{audience: zitProject},
			want: []string{"admin", "editor"},
		},
		{
			name: "zitadel multi org: a role granted in two orgs appears once",
			claims: map[string]any{
				zitProjectClaim: zitadelRoles(map[string][]string{
					"admin":  {"org-1", "org-2"},
					"viewer": {"org-2"},
				}),
			},
			opts: claimOpts{audience: zitProject},
			want: []string{"admin", "viewer"},
		},
		{
			name: "zitadel with an audience ignores another project's roles and the aggregate",
			claims: map[string]any{
				zitOtherClaim: zitadelRoles(map[string][]string{"superuser": {"org-1"}}),
				zitAggregate:  zitadelRoles(map[string][]string{"superuser": {"org-1"}}),
			},
			opts: claimOpts{audience: zitProject},
			want: nil,
		},
		{
			name: "zitadel without an audience merges every project in the token",
			claims: map[string]any{
				zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"org-1"}}),
				zitOtherClaim:   zitadelRoles(map[string][]string{"auditor": {"org-1"}}),
				zitAggregate:    zitadelRoles(map[string][]string{"admin": {"org-1"}, "auditor": {"org-1"}}),
			},
			want: []string{"admin", "auditor"},
		},
		{
			name: "zitadel aggregate claim alone is honored when no audience is set",
			claims: map[string]any{
				zitAggregate: zitadelRoles(map[string][]string{"admin": {"org-1"}}),
			},
			want: []string{"admin"}, wantRole: "admin",
		},
		{
			name: "zitadel claim present but empty",
			claims: map[string]any{
				zitProjectClaim: map[string]any{},
			},
			opts: claimOpts{audience: zitProject},
			want: nil,
		},
		{
			name: "a flat roles array beats zitadel claims in auto mode",
			claims: map[string]any{
				"roles":         []any{"member"},
				zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"org-1"}}),
			},
			opts: claimOpts{audience: zitProject},
			want: []string{"member"},
		},
		{
			name: "a token's own `role` is not overwritten by a single derived role",
			claims: map[string]any{
				"role":          "owner",
				zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"org-1"}}),
			},
			opts: claimOpts{audience: zitProject},
			want: []string{"admin"}, wantRole: "owner",
		},
		{
			name: "explicit claim: reads exactly that claim, nothing else",
			claims: map[string]any{
				"roles":         []any{"ignored"},
				zitProjectClaim: zitadelRoles(map[string][]string{"ignored-too": {"org-1"}}),
				"groups":        []any{"g1", "g2"},
			},
			opts: claimOpts{rolesClaim: "groups", audience: zitProject},
			want: []string{"g1", "g2"},
		},
		{
			name: "explicit claim naming a zitadel role map",
			claims: map[string]any{
				zitOtherClaim: zitadelRoles(map[string][]string{"auditor": {"org-1"}}),
			},
			opts: claimOpts{rolesClaim: zitOtherClaim},
			want: []string{"auditor"}, wantRole: "auditor",
		},
		{
			name:   "explicit claim as a space separated string (OAuth scope style)",
			claims: map[string]any{"scp": "read write"},
			opts:   claimOpts{rolesClaim: "scp"},
			want:   []string{"read", "write"},
		},
		{
			name:   "explicit claim that is absent",
			claims: map[string]any{"roles": []any{"x"}},
			opts:   claimOpts{rolesClaim: "groups"},
			want:   nil,
		},
		{
			name:   "explicit `auto` is the default",
			claims: map[string]any{zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"o"}})},
			opts:   claimOpts{rolesClaim: "AUTO", audience: zitProject},
			want:   []string{"admin"}, wantRole: "admin",
		},
		{
			name:   "explicit claim `roles` keeps Role untouched",
			claims: map[string]any{"roles": []any{"only"}},
			opts:   claimOpts{rolesClaim: "roles"},
			want:   []string{"only"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultProjectClaims(tc.claims, tc.opts)
			if !reflect.DeepEqual(c.Roles, tc.want) {
				t.Errorf("Roles = %#v, want %#v", c.Roles, tc.want)
			}
			if c.Role != tc.wantRole {
				t.Errorf("Role = %q, want %q", c.Role, tc.wantRole)
			}
		})
	}
}

// The mapping must reach Claims through a real validator, not just the helper:
// once through the single-issuer path and once through the TokenValidators
// chain, which are separate code paths.
func TestRoles_ThroughValidators(t *testing.T) {
	key := rsaKeyFor(t)
	const kid = "k1"
	url := newJWKSServer(t, kid, rsaJWK(t, key, kid))
	tok := signRS256WithKID(t, key, kid, jwt.MapClaims{
		"sub": "u1", "iss": "https://i.example", "aud": []string{zitProject, "client-1"},
		"exp":           time.Now().Add(time.Hour).Unix(),
		zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"org-1"}, "editor": {"org-1"}}),
	})

	v, err := NewValidator(Config{Provider: ProviderJWT, JWT: JWTConfig{
		SigningMethod: "RS256", JWKSURL: url, Issuer: "https://i.example", Audience: zitProject,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	c, err := v.Validate(tok)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"admin", "editor"}; !reflect.DeepEqual(c.Roles, want) {
		t.Errorf("single-issuer Roles = %v, want %v", c.Roles, want)
	}

	kf, err := JWKSKeyfunc(url)
	if err != nil {
		t.Fatal(err)
	}
	jv, err := NewJWKSValidator(JWKSValidatorConfig{KeyFunc: kf, Issuer: "https://i.example", Audience: zitProject})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := jv.ValidateToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"admin", "editor"}; !reflect.DeepEqual(cc.Roles, want) {
		t.Errorf("JWKSValidator Roles = %v, want %v", cc.Roles, want)
	}

	// An explicit claim configured on the validator is honored on both paths.
	v2, err := NewValidator(Config{Provider: ProviderJWT, JWT: JWTConfig{
		SigningMethod: "RS256", JWKSURL: url, Audience: zitProject, RolesClaim: zitOtherClaim,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v2.Close() })
	c2, err := v2.Validate(tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Roles) != 0 {
		t.Errorf("explicit RolesClaim naming an absent claim should yield no roles, got %v", c2.Roles)
	}
}

// Clerk and Firebase resolvers are untouched by the new mapping.
func TestRoles_ClerkAndFirebaseUnchanged(t *testing.T) {
	mc := map[string]any{
		"sub": "u", "org_role": "org:admin", "org_permissions": []any{"org:read"},
		zitProjectClaim: zitadelRoles(map[string][]string{"admin": {"o"}}),
	}
	c, err := clerkResolver{}.Resolve(mc)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"org:read", "org:admin"}; !reflect.DeepEqual(c.Roles, want) || c.Role != "org:admin" {
		t.Errorf("clerk: Roles=%v Role=%q", c.Roles, c.Role)
	}

	fc, err := firebaseResolver{}.Resolve(map[string]any{"sub": "u", "roles": []any{"a"}, "role": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fc.Roles, []string{"a"}) || fc.Role != "a" {
		t.Errorf("firebase: Roles=%v Role=%q", fc.Roles, fc.Role)
	}
}
