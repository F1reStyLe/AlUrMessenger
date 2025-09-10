package postgres

import (
	"alurmsg/internal/domain"
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

type PostgresChatRepository struct {
	db *sqlx.DB
}

func NewPostgresChatRepository(db *sqlx.DB) *PostgresChatRepository {
	return &PostgresChatRepository{db: db}
}

func (r *PostgresChatRepository) CreateChat(ctx context.Context, chat *domain.Chat) (int, error) {
	chatTypeID, err := r.GetOrCreateChatType(ctx, &chat.Type)

	if err != nil {
		return 0, err
	}

	tx, err := r.db.BeginTxx(ctx, nil)

	if err != nil {
		return 0, err
	}

	defer tx.Rollback()

	chat.ID, err = r.createChat(ctx, tx, chat, chatTypeID)

	if err != nil {
		return 0, err
	}

	err = r.addUserToChat(ctx, tx, chat)

	if err != nil {
		return 0, err
	}

	tx.Commit()

	return chat.ID, nil
}

func (r *PostgresChatRepository) createChat(ctx context.Context, tx *sqlx.Tx, chat *domain.Chat, chatTypeID int) (int, error) {
	err := tx.QueryRowContext(ctx, `
    INSERT INTO public.chats (type, name)
    VALUES ($1, $2)
    returning id;`,
		chatTypeID, chat.Name).Scan(&chat.ID)

	if err != nil {
		return 0, err
	}

	return chat.ID, nil
}

func (r *PostgresChatRepository) AddUserToChat(ctx context.Context, chat *domain.Chat) error {
	isPrivate, err := r.isPrivateChat(ctx, chat.ID)

	if err != nil {
		return err
	}

	if isPrivate {
		return fmt.Errorf("cannot add user to private chat")
	}

	tx, err := r.db.BeginTxx(ctx, nil)

	if err != nil {
		return err
	}

	defer tx.Rollback()

	err = r.addUserToChat(ctx, tx, chat)

	if err != nil {
		return err
	}

	tx.Commit()

	return nil
}

func (r *PostgresChatRepository) addUserToChat(ctx context.Context, tx *sqlx.Tx, chat *domain.Chat) error {
	for _, userID := range chat.Members {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO public.chat_members (chat_id, user_id)
			VALUES ($1, $2)
			ON CONFLICT (chat_id, user_id)
			DO NOTHING;`,
			&chat.ID, userID)

		if err != nil {
			return err
		}
	}

	return nil
}

func (r *PostgresChatRepository) isPrivateChat(ctx context.Context, chatID int) (bool, error) {
	var isPrivate bool

	err := r.db.QueryRowContext(ctx, `
      select ct."name" = 'private'
      from public.chats ch
      join public.chat_types ct on ct.id = ch."type"
      where ch.id = $1
      `, chatID).Scan(&isPrivate)

	if err != nil {
		return false, err
	}

	return isPrivate, nil
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
