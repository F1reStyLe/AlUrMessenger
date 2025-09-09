package postgres

import (
	"alurmsg/internal/domain"
	"context"

	"github.com/jmoiron/sqlx"
)

type PostgresMessageRepository struct {
	db *sqlx.DB
}

func NewPostgresMessageRepository(db *sqlx.DB) *PostgresMessageRepository {
	return &PostgresMessageRepository{db: db}
}

func (r *PostgresMessageRepository) SaveMessage(ctx context.Context, message *domain.Message) (int, error) {
	err := r.db.QueryRowContext(ctx, `
      INSERT INTO public.messages (chat_id, user_id, content)
      VALUES ($1, $2, $3)
      returning id`,
		message.ChatID, message.UserID, message.Text).Scan(message.ID)

	if err != nil {
		return 0, err
	}

	return message.ID, err
}
