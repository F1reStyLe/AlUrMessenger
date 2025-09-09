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
	chatType, err := r.GetOrCreateChatType(ctx, &chat.Type)

	if err != nil {
		return 0, err
	}

	tx, err := r.db.BeginTx(ctx, nil)

	if err != nil {
		return 0, err
	}

	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `
    INSERT INTO public.chats (type, name)
    VALUES ($1, $2)
    returning id;`,
		chatType, chat.Name).Scan(&chat.ID)

	if err != nil {
		return 0, err
	}

	for _, userID := range chat.Members {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO public.chat_members (chat_id, user_id)
			VALUES ($1, $2)
			ON CONFLICT (chat_id, user_id)
			DO NOTHING;`,
			&chat.ID, userID)

		if err != nil {
			return 0, err
		}
	}

	if err != nil {
		return 0, err
	}

	tx.Commit()

	return chat.ID, nil
}

func (r *PostgresChatRepository) AddUserToChat(ctx context.Context, chatID int, userID int) error {
	_, err := r.db.ExecContext(ctx, `
			insert into public.chat_members(chat_id, user_id)
			values ($1, $2)
			on conflict(chat_id, user_id) do nothing;
			`,
		chatID, userID)

	return err
}

func (r *PostgresChatRepository) GetOrCreateChatType(ctx context.Context, chatType *domain.ChatType) (int, error) {
	var ID int
	err := r.db.QueryRowContext(ctx, `
      insert into public.chat_types(name)
      values ($1)
      on conflict(name) do update
      set name = $1
      returning id;
			`,
		&chatType).Scan(&ID)

	if err != nil {
		return 0, err
	}

	return ID, err
}
