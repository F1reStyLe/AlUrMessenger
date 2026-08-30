// Package event defines the reference-only durable event contract, independent of storage.
package event

import "time"

// Envelope is the stable Kafka/replay contract; payload must not contain message content.
type Envelope struct {
	ID            string         `json:"event_id"`
	Type          string         `json:"event_type"`
	Version       int            `json:"event_version"`
	ProjectID     string         `json:"project_id"`
	AggregateType string         `json:"aggregate_type"`
	AggregateID   string         `json:"aggregate_id"`
	Sequence      int64          `json:"aggregate_sequence,string"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Payload       map[string]any `json:"payload"`
}
