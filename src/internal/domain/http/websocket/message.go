package websocket

import "time"

const (
	MessageTypeChat    = "chat"
	MessageTypeLike    = "like"
	MessageTypeComment = "comment"
	MessageTypeSystem  = "system"
)

type Message struct {
	ID        int         `json:"id,omitempty"`
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
	Error     string      `json:"error,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

type ChatPayload struct {
	User int    `json:"user"`
	Chat int    `json:"chat"`
	Text string `json:"text"`
}

type LikePayload struct {
	UserID   int    `json:"user_id"`
	PostID   int    `json:"post_id"`
	UserName string `json:"user_name"`
}

type CommentPayload struct {
	User    string `json:"user"`
	Content string `json:"content"`
	PostID  int    `json:"post_id"`
}

type SystemPayload struct {
	Event string `json:"event"`
	Count int    `json:"count,omitempty"`
}
