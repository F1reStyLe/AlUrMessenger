package auth

import "errors"

var (
	ErrInvalidCredentials  = errors.New("invalid username or password")
	ErrUserNotFound        = errors.New("user not found")
	ErrUserExists          = errors.New("user already exists")
	ErrPasswordTooWeak     = errors.New("password is too weak")
	ErrRefreshTokenInvalid = errors.New("refresh token is invalid or expired")
	ErrRefreshTokenRevoked = errors.New("refresh token has been revoked")
)
