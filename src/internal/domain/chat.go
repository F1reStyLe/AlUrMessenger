package domain

import "time"

type ChatType string

const (
	ChatTypePrivate ChatType = "private"
	ChatTypeGroup   ChatType = "group"
)

type Chat struct {
	ID        int       `json:"id"`
	Type      ChatType  `json:"type"`
	Name      *string   `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Members   []User    `json:"members"`
}
