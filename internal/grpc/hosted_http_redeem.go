package grpc

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/hosteduse"
)

// NewHostedHTTPRedeemHandler is the controller-only one-use ticket route. It
// accepts the existing short-lived internal-service JWT, never a user JWT/API
// key. An absent shared secret or store leaves the handler closed.
func NewHostedHTTPRedeemHandler(store hosteduse.Store, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret == "" || store == nil || !validHostedRedeemBearer(r.Header.Get("Authorization"), secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request hosteduse.RedeemRequest
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		metadata, err := hosteduse.Redeem(r.Context(), store, request)
		if err != nil {
			http.Error(w, "ticket denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metadata)
	})
}

func validHostedRedeemBearer(header, secret string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("control-plane"), jwt.WithAudience("authenticated"), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid || claims["sub"] != auth.InternalServiceSubject || claims["role"] != auth.InternalServiceRole {
		return false
	}
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil {
		return false
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return false
	}
	return exp.After(iat.Time) && exp.Sub(iat.Time) <= auth.InternalServiceTokenTTL && time.Since(iat.Time) <= auth.InternalServiceTokenTTL
}
