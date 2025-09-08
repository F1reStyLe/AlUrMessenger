package handlers

import (
	"alurmsg/internal/domain"
	"alurmsg/internal/repository"
	"encoding/json"
	"net/http"
)

type ChatHandler struct {
	ChatRepo repository.ChatRepository
}

func NewChatHandler(chatRepo repository.ChatRepository) *ChatHandler {
	return &ChatHandler{ChatRepo: chatRepo}
}

func (h *ChatHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	var chat *domain.Chat
	ctx := r.Context()
	err := json.NewDecoder(r.Body).Decode(&chat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := h.ChatRepo.CreateChat(ctx, chat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int{"id": id})
}
