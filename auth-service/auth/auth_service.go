package auth

import (
	"auth/domain"
	"auth/repository"
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/bcrypt"
)

type JWTClaims struct {
	Userid      string `json:"userid"`
	Username    string `json:"username"`
	Permissions int    `json:"permissions"`
	jwt.RegisteredClaims
}

type AuthService struct {
	userRepo        repository.UserRepository
	tokenRepo       repository.TokenRepository
	jwtSecret       []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewAuthService(userRepo repository.UserRepository, tokenRepo repository.TokenRepository, jwtSecret string, ttl time.Duration, refreshttl time.Duration) *AuthService {
	return &AuthService{
		userRepo:        userRepo,
		tokenRepo:       tokenRepo,
		jwtSecret:       []byte(jwtSecret),
		accessTokenTTL:  ttl,
		refreshTokenTTL: refreshttl,
	}
}

func (s *AuthService) Authenticate(username string, email string, password string) (*domain.Tokens, error) {
	ctx := context.Background()

	// Получаем пользователя
	user, err := s.userRepo.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}

	// Проверяем пароль
	err = bcrypt.CompareHashAndPassword([]byte(user.HashedPassword), []byte(password))
	if err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	token, err := s.GenerateTokens(ctx, user)
	if err != nil {
		return nil, err
	}

	return token, nil
}

func (s *AuthService) GetUser(userid string) (*domain.User, error) {
	ctx := context.Background()
	return s.userRepo.GetUser(ctx, userid)
}

func (s *AuthService) GenerateTokens(ctx context.Context, user *domain.User) (*domain.Tokens, error) {
	accessToken, err := s.generateToken(user.ID, user.Username, user.Permissions, 3600*time.Second)
	if err != nil {
		return nil, err
	}

	refreshToken, err := s.generateRefreshToken(user.Username, user.ID)
	if err != nil {
		return nil, err
	}

	err = s.tokenRepo.Save(ctx, user.ID, refreshToken)
	if err != nil {
		return nil, err
	}

	var token = &domain.Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}

	return token, nil
}

func (s *AuthService) Register(user *domain.User) error {
	ctx := context.Background()

	// Проверяем, существует ли пользователь
	err := s.userRepo.CreateUser(ctx, user)
	if err != nil {
		return fmt.Errorf("error to create user %s", err)
	}

	return nil
}

func (s *AuthService) SaveToken(userid, token string) error {
	ctx := context.Background()
	return s.tokenRepo.Save(ctx, userid, token)
}

func (s *AuthService) RevokeToken(token string) error {
	ctx := context.Background()
	return s.tokenRepo.Revoke(ctx, token)
}

func (s *AuthService) ValidateToken(token string) (string, error) {
	ctx := context.Background()
	return s.tokenRepo.Validate(ctx, token)
}

func (s *AuthService) generateToken(userid string, username string, permissions int, expires time.Duration) (string, error) {
	claims := &JWTClaims{
		Userid:      userid,
		Username:    username,
		Permissions: permissions,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expires)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

func (s *AuthService) generateRefreshToken(username, userid string) (string, error) {
	claims := &JWTClaims{
		Userid:   userid,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.refreshTokenTTL)),
			Subject:   "refresh", // можно добавить тип
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

func (s *AuthService) ParseWithClaims(refreshToken string, claims jwt.Claims) (*jwt.Token, error) {
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (interface{}, error) {
		return s.jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	return token, nil
}
