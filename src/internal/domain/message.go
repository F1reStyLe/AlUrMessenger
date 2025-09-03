package domain

import "time"

type Message struct {
	ID        int       `json:"id"`
	ChatID    int       `json:"chatid"`
	UserID    int       `json:"userid"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IsEdited  bool      `json:"is_edited"`
}
