// Package repository appends event log and outbox in the caller's PostgreSQL transaction.
package repository

import (
	"context"
	"encoding/json"
	"github.com/F1reStyLe/AlUrMessenger/internal/event"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

// Append increments the aggregate cursor and inserts both replay and outbox rows.
// Caller already holds the conversation lock and must commit or roll back everything.
func Append(ctx context.Context, tx pgx.Tx, project, conversation, kind string, payload map[string]any) (event.Envelope, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return event.Envelope{}, err
	}
	e := event.Envelope{ID: id.String(), Type: kind, Version: 1, ProjectID: project, AggregateType: "conversation", AggregateID: conversation, OccurredAt: time.Now().UTC(), Payload: payload}
	if err = tx.QueryRow(ctx, "UPDATE chat.conversations SET event_sequence=event_sequence+1 WHERE project_id=$1 AND id=$2 RETURNING event_sequence", project, conversation).Scan(&e.Sequence); err != nil {
		return e, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.conversation_events(id,project_id,conversation_id,sequence,envelope,occurred_at) VALUES($1,$2,$3,$4,$5,$6)", e.ID, project, conversation, e.Sequence, data, e.OccurredAt); err != nil {
		return e, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO chat.outbox_events(event_id,project_id,conversation_id,sequence,envelope) VALUES($1,$2,$3,$4,$5)", e.ID, project, conversation, e.Sequence, data)
	return e, err
}
