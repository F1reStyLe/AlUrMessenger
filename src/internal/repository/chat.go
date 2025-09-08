package repository

import (
	"alurmsg/internal/domain"
	"context"
)

type ChatRepository interface {
	CreateChat(ctx context.Context, chat *domain.Chat) (int, error)
}
