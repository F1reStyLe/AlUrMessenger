package domain

import "time"

type Message struct {
	ID        int       `json:"id"`
	Chat      Chat      `json:"chat"`
	User      User      `json:"user"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IsEdited  bool      `json:"is_edited"`
}
