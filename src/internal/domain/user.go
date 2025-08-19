package domain

import "time"

type User struct {
	ID        int        `json:"id"`
	Name      string     `json:"name" validate:"required, min=3, max=20"`
	Email     string     `json:"email" validate:"required, email"`
	Password  string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	IsOnline  bool       `json:"is_online"`
}
