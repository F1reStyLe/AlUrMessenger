package repository

import (
	"context"
	"fmt"

	"github.com/alexedwards/argon2id"
	"github.com/golang-jwt/jwt/v4"
	"github.com/jmoiron/sqlx"
)

type TokenRepository interface {
	Save(ctx context.Context, userid string, token string, expiration *jwt.NumericDate) error
	Revoke(ctx context.Context, token string) error
	Validate(ctx context.Context, token string) (string, error)
}

type PostgresTokenRepository struct {
	db *sqlx.DB
}

func NewPostgresTokenRepository(db *sqlx.DB) *PostgresTokenRepository {
	return &PostgresTokenRepository{db: db}
}

func (r *PostgresTokenRepository) Save(ctx context.Context, userid string, token string, expiration *jwt.NumericDate) error {
	if len(token) == 0 {
		return fmt.Errorf("token is empty")
	}

	hashedtoken, err := argon2id.CreateHash(token, argon2id.DefaultParams)

	if err != nil {
		return fmt.Errorf("failed to hash token: %v", err)
	}

	if len(hashedtoken) == 0 {
		return fmt.Errorf("hashed token is empty")
	}

	_, err = r.db.ExecContext(ctx, "INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)", userid, hashedtoken, expiration.Time)

	if err != nil {
		return fmt.Errorf("failed to save token: %v", err)
	}

	return nil
}

func (r *PostgresTokenRepository) Revoke(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE token_hash = $1", token)
	return err
}

func (r *PostgresTokenRepository) Validate(ctx context.Context, token string) (string, error) {
	var userid string
	var storedHash string

	err := r.db.QueryRowContext(ctx,
		"SELECT user_id, token_hash FROM refresh_tokens WHERE token_hash = $1",
		token,
	).Scan(&userid, &storedHash)
	if err != nil {
		return "", err
	}

	ok, err := argon2id.ComparePasswordAndHash(token, storedHash)

	if !ok || err != nil {
		return "", fmt.Errorf("invalid token")
	}

	return userid, nil
}
