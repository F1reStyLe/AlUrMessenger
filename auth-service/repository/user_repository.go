package repository

import (
	"auth/domain"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

type UserRepository interface {
	GetUser(ctx context.Context, username string) (*domain.User, error)
	CreateUser(ctx context.Context, user *domain.User) error
}

type PostgresUserRepository struct {
	db *sqlx.DB
}

func NewPostgresUserRepository(db *sqlx.DB) *PostgresUserRepository {
	return &PostgresUserRepository{db: db}
}

func (r *PostgresUserRepository) GetUser(ctx context.Context, username string) (*domain.User, error) {
	var user domain.User

	err := r.db.QueryRowContext(ctx,
		`SELECT id, password_hash FROM users
		WHERE name = $1`,
		username,
	).Scan(&user.ID, &user.HashedPassword)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("user not found: %w", err)
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	user.Username = username

	return &user, nil
}

func (r *PostgresUserRepository) CreateUser(ctx context.Context, user *domain.User) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (name, password_hash, email)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
		SET password_hash = $2, email = $3`,
		user.Username, user.HashedPassword, user.Email,
	)

	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	return nil
}
