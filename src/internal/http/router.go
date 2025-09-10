package rest

import (
	"alurmsg/internal/config"
	"alurmsg/internal/handlers"
	"alurmsg/internal/repository/postgres"
	"net/http"

	"github.com/jmoiron/sqlx"
)

func RegisterHandlers(mux *http.ServeMux, cfg *config.Config, db *sqlx.DB, authMiddleware func(http.HandlerFunc) http.HandlerFunc) {
	chatRepo := postgres.NewPostgresChatRepository(db)
	chatHandler := handlers.NewChatHandler(chatRepo)
	mux.Handle("POST /api/chats", authMiddleware(chatHandler.CreateChat))
	mux.Handle("POST /api/chats/adduser", authMiddleware(chatHandler.AddUserToChat))
}
