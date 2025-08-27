package handlers

import (
	"auth/auth"
	"auth/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/alexedwards/argon2id"
)

type AuthHandler struct {
	Service *auth.AuthService
}

func NewAuthHandler(service *auth.AuthService) *AuthHandler {
	return &AuthHandler{Service: service}
}

// Register обрабатывает регистрацию нового пользователя
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req domain.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	// Хешируем пароль
	hashed, err := argon2id.CreateHash(req.Password, argon2id.DefaultParams)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Создаём пользователя
	user := &domain.User{
		Username:       req.Username,
		HashedPassword: string(hashed),
		Email:          req.Email,
	}

	err = h.Service.Register(user)
	if err != nil {
		http.Error(w, "Failed to register user", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"message": "User registered successfully"})
}

// Login обрабатывает аутентификацию пользователя
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req domain.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	tokens, err := h.Service.Authenticate(req.Username, req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		default:
			log.Printf("Auth error: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Устанавливаем refresh_token в куку
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    tokens.RefreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // ⚠️ В продакшене true
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 86400,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"access_token": tokens.AccessToken,
	})
}

// Logout обрабатывает выход пользователя
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		// Отзываем текущий refresh_token
		h.Service.RevokeToken(cookie.Value)
	}

	// Удаляем куки
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Logged out successfully",
	})
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		http.Error(w, "Refresh token required", http.StatusUnauthorized)
		return
	}
	refreshToken := cookie.Value

	// Парсим JWT
	claims := &auth.JWTClaims{}
	token, err := h.Service.ParseWithClaims(refreshToken, claims)

	if err != nil || !token.Valid {
		http.Error(w, "Invalid refresh token", http.StatusUnauthorized)
		return
	}

	// Проверяем, что токен не отозван
	userid, err := h.Service.ValidateToken(refreshToken)
	if err != nil {
		http.Error(w, "Invalid or revoked refresh token", http.StatusUnauthorized)
		return
	}

	// Отзываем старый токен
	if err := h.Service.RevokeToken(refreshToken); err != nil {
		log.Printf("Failed to revoke token: %v", err)
	}

	user, err := h.Service.GetUser(userid)

	if err != nil {
		http.Error(w, "Failed to get user", http.StatusInternalServerError)
		return
	}

	ctx := context.Background()
	tokens, err := h.Service.GenerateTokens(ctx, user)

	if err != nil {
		http.Error(w, "Failed to generate tokens", http.StatusInternalServerError)
		return
	}

	// Устанавливаем куку
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    tokens.RefreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 86400,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"access_token": tokens.AccessToken,
	})
}

func (h *AuthHandler) WhoAmI(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value("claims").(*auth.JWTClaims)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"userid":      claims.Userid,
		"username":    claims.Username,
		"permissions": fmt.Sprintf("%d", claims.Permissions),
	})
}
