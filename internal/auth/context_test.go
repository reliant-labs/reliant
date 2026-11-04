// Copyright (c) 2025 Reliant Labs
package auth

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func fakeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"sub":"u","exp":%d}`, exp.Unix()))) + ".sig"
}

// An expired cached JWT is not a signed-in session: returning it made an
// unattended run present it to the control plane ("token expired") instead
// of falling back to its stored automation token.
func TestGetUserJWTDropsAnExpiredToken(t *testing.T) {
	live := fakeJWT(t, time.Now().Add(time.Hour))
	SetUserJWT("user-live", live)
	got, ok := GetUserJWT("user-live")
	assert.True(t, ok)
	assert.Equal(t, live, got)

	SetUserJWT("user-expired", fakeJWT(t, time.Now().Add(-time.Minute)))
	got, ok = GetUserJWT("user-expired")
	assert.False(t, ok, "an expired JWT must read as no session")
	assert.Empty(t, got)

	_, ok = GetUserJWT("user-never-seen")
	assert.False(t, ok)
}

func TestJWTExpiredJudgesOnlyTokensItCanRead(t *testing.T) {
	now := time.Now()
	assert.True(t, jwtExpired(fakeJWT(t, now.Add(-time.Second)), now))
	assert.True(t, jwtExpired(fakeJWT(t, now), now), "exp is exclusive")
	assert.False(t, jwtExpired(fakeJWT(t, now.Add(time.Second)), now))
	for _, opaque := range []string{"", "test-jwt", "rlat_abc", "a.b", "a.!!!.c"} {
		assert.False(t, jwtExpired(opaque, now), "%q is not a readable JWT, so it is not judged", opaque)
	}
	noExp := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u"}`))
	assert.False(t, jwtExpired("h."+noExp+".s", now))
}
