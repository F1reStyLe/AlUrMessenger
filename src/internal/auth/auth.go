package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey struct {
	name string
}

var userCtxKey = &contextKey{"user"}

type JWTClaims struct {
	Userid      string `json:"userid"`
	Username    string `json:"username"`
	Permissions int    `json:"permissions"`
	jwt.RegisteredClaims
}

// Middleware для проверки аутентификации WebSocket соединения
func AuthMiddleware(jwtSecret []byte) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			authtoken := r.Header.Get("Authorization")

			if authtoken == "" {
				authtoken = r.URL.Query().Get("Authorization")
				if authtoken == "" {
					http.Error(w, "Authentication required", http.StatusUnauthorized)
					return
				}
			}

			var tokenString string
			if strings.HasPrefix(authtoken, "Bearer ") {
				tokenString = strings.TrimPrefix(authtoken, "Bearer ")
			} else {
				tokenString = authtoken
			}

			claims := &JWTClaims{}
			token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return jwtSecret, nil
			})

			if err != nil {
				switch {
				case err == jwt.ErrSignatureInvalid:
					http.Error(w, "Invalid token signature", http.StatusUnauthorized)
				case err == jwt.ErrTokenExpired:
					http.Error(w, "Token has expired", http.StatusUnauthorized)
				default:
					http.Error(w, "Invalid token", http.StatusUnauthorized)
				}
				return
			}

			if !token.Valid {
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userCtxKey, claims)
			r = r.WithContext(ctx)

			next.ServeHTTP(w, r)
		}
	}
}
