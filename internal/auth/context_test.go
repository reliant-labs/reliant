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

// A run's activities offer the token captured at its start to whichever
// worker runs them: it fills an empty or expired slot, and never replaces a
// live token that lasts longer.
func TestOfferUserJWTNeverDowngradesALiveToken(t *testing.T) {
	now := time.Now()
	older, fresher := fakeJWT(t, now.Add(10*time.Minute)), fakeJWT(t, now.Add(time.Hour))
	// The cache is process-global: every user id is this invocation's own,
	// so a repeated run (-count, the flake gate) starts from empty slots.
	user := func(name string) string { return fmt.Sprintf("%s-%s-%d", t.Name(), name, now.UnixNano()) }

	empty := user("empty")
	OfferUserJWT(empty, older)
	got, ok := GetUserJWT(empty)
	assert.True(t, ok)
	assert.Equal(t, older, got, "an empty slot takes the offered token")

	fresherHeld := user("fresher-held")
	SetUserJWT(fresherHeld, fresher)
	OfferUserJWT(fresherHeld, older)
	got, _ = GetUserJWT(fresherHeld)
	assert.Equal(t, fresher, got, "a live token that lasts longer is kept")

	OfferUserJWT(empty, fresher)
	got, _ = GetUserJWT(empty)
	assert.Equal(t, fresher, got, "a token that lasts longer replaces the held one")

	expiredHeld := user("expired-held")
	SetUserJWT(expiredHeld, fakeJWT(t, now.Add(-time.Minute)))
	OfferUserJWT(expiredHeld, older)
	got, ok = GetUserJWT(expiredHeld)
	assert.True(t, ok)
	assert.Equal(t, older, got, "an expired token is replaced")

	opaque := user("opaque")
	SetUserJWT(opaque, "opaque-a")
	OfferUserJWT(opaque, "opaque-b")
	got, _ = GetUserJWT(opaque)
	assert.Equal(t, "opaque-b", got, "unreadable expiries are not compared: the offer wins, as SetUserJWT does")

	blank := user("blank")
	OfferUserJWT("", older)
	OfferUserJWT(blank, " ")
	_, ok = GetUserJWT(blank)
	assert.False(t, ok, "a blank token is not offered")
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
