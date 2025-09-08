package postgres

import (
	"alurmsg/internal/domain"
	"context"

	"github.com/jmoiron/sqlx"
)

type PostgresChatRepository struct {
	db *sqlx.DB
}

func NewPostgresChatRepository(db *sqlx.DB) *PostgresChatRepository {
	return &PostgresChatRepository{db: db}
}

func (r *PostgresChatRepository) CreateChat(ctx context.Context, chat *domain.Chat) (int, error) {
	chatType, err := r.GetOrCreateChatType(ctx, chat.Type)

	if err != nil {
		return 0, err
	}

	err = r.db.QueryRowContext(ctx, `
    INSERT INTO chats (type, name)
    VALUES ($1, $2)
    returning id`,
		chatType, chat.Name).Scan(&chat.ID)

	if err != nil {
		return 0, err
	}

	return chat.ID, nil
}

func (r *PostgresChatRepository) GetOrCreateChatType(ctx context.Context, chatType domain.ChatType) (int, error) {
	var ID int
	err := r.db.QueryRowContext(ctx, `
      insert into chat_types(type_name)
      values ($1)
      on conflict(type_name) do update
      set type_name = $1
      returning id
			`,
		chatType).Scan(&ID)

	if err != nil {
		return 0, err
	}

	return ID, err
}
