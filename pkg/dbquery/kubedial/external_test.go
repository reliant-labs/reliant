package kubedial

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExternalConnConfig(t *testing.T) {
	ctx := context.Background()
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "ledger-ro"},
		Data: map[string][]byte{"password": []byte("pw"), "other": []byte("x")}}
	x := External{Namespace: "prod", Service: "pgbouncer", Database: "ledger_cp", Username: "ledger_ro", PasswordSecret: "ledger-ro"}

	cfg, err := NewWithClientset(fake.NewClientset(sec), nil).ExternalConnConfig(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "ledger_ro" || cfg.Password != "pw" || cfg.Database != "ledger_cp" || cfg.Host != "pgbouncer.prod.svc" {
		t.Errorf("config %s@%s/%s", cfg.User, cfg.Host, cfg.Database)
	}
	if cfg.TLSConfig == nil || !cfg.TLSConfig.InsecureSkipVerify || cfg.DialFunc == nil {
		t.Errorf("want require (TLS, unverified) through the tunnel: tls=%v dial=%v", cfg.TLSConfig, cfg.DialFunc != nil)
	}

	disabled := x
	disabled.SSLMode = SSLModeDisable
	if cfg, err := NewWithClientset(fake.NewClientset(sec), nil).ExternalConnConfig(ctx, disabled); err != nil || cfg.TLSConfig != nil {
		t.Errorf("disable: %v %v", err, cfg)
	}

	otherKey := x
	otherKey.PasswordKey = "other"
	if cfg, err := NewWithClientset(fake.NewClientset(sec), nil).ExternalConnConfig(ctx, otherKey); err != nil || cfg.Password != "x" {
		t.Errorf("password_key: %v", err)
	}

	for name, tc := range map[string]struct {
		x    External
		objs []corev1.Secret
		want string
	}{
		"no secret":   {x: x, want: "prod/ledger-ro not found"},
		"no key":      {x: External{Namespace: "prod", Service: "pgbouncer", Database: "d", Username: "u", PasswordSecret: "ledger-ro", PasswordKey: "nope"}, objs: []corev1.Secret{*sec}, want: `no "nope" key`},
		"bad sslmode": {x: External{Namespace: "prod", Service: "pgbouncer", Database: "d", Username: "u", PasswordSecret: "ledger-ro", SSLMode: SSLModeVerifyFull}, objs: []corev1.Secret{*sec}, want: "sslmode"},
		"incomplete":  {x: External{Namespace: "prod", Service: "pgbouncer"}, want: "needs"},
	} {
		cs := fake.NewClientset()
		for i := range tc.objs {
			if _, err := cs.CoreV1().Secrets("prod").Create(ctx, &tc.objs[i], metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := NewWithClientset(cs, nil).ExternalConnConfig(ctx, tc.x)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: the error carries the password: %v", name, err)
		}
	}
}
