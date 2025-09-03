package repository

import (
	"alurmsg/internal/domain"
	"context"
)

type MessageRepository interface {
	SaveMessage(ctx context.Context, message *domain.Message) (int, error)
}
