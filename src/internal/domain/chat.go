package domain

import "time"

type ChatType string

const (
	ChatTypePrivate ChatType = "private"
	ChatTypeGroup   ChatType = "group"
)

type Chat struct {
	ID        int       `json:"id,omitempty"`
	Type      ChatType  `json:"type"`
	Name      string    `json:"name,omitempty"`
	Members   []int     `json:"members,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
