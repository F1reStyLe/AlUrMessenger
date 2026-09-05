package repository

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reports owns encrypted complaint descriptions. Authorization is deliberately
// repeated in SQL transactions so stale JWT-derived actors cannot bypass a ban,
// Project suspension, membership removal, or an admin-role change.
type Reports struct {
	DB     *pgxpool.Pool
	Crypto moderation.ReportCrypto
}

const reportProjection = `SELECT id::text,reporter_id::text,target_user_id::text,reported_message_id::text,conversation_id::text,
 reason,encrypted_description,nonce,key_version,payload_version,status,resource_version,reviewed_by::text,reviewed_at,created_at,updated_at
 FROM chat.reports WHERE project_id=$1`

type reportRow interface{ Scan(...any) error }

func (s *Reports) scan(project string, row reportRow) (moderation.ReviewedReport, error) {
	var result moderation.ReviewedReport
	var target, message, conversation, reviewer *string
	var ciphertext, nonce []byte
	var keyVersion string
	var payloadVersion int
	err := row.Scan(&result.ID, &result.ReporterID, &target, &message, &conversation, &result.Reason,
		&ciphertext, &nonce, &keyVersion, &payloadVersion, &result.Status, &result.Version,
		&reviewer, &result.ReviewedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, identity.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	plain, err := s.Crypto.OpenReport(project, result.ID, cryptography.Envelope{
		Ciphertext: ciphertext, Nonce: nonce, KeyVersion: keyVersion, PayloadVersion: payloadVersion,
	})
	if err != nil || !utf8.Valid(plain) {
		return result, cryptography.ErrCrypto
	}
	result.TargetUserID, result.MessageID, result.ConversationID = target, message, conversation
	result.Description, result.ReviewedBy = string(plain), reviewer
	return result, nil
}

// reporterTx establishes the immutable Project-first lock order shared by all
// write paths, then enforces the human/unbanned reporter rule from current data.
func reporterTx(ctx context.Context, tx pgx.Tx, actor identity.Actor) error {
	var active, human, banned bool
	err := tx.QueryRow(ctx, `SELECT p.status='active',u.kind='human',u.banned_at IS NOT NULL
 FROM chat.projects p JOIN chat.users u ON u.project_id=p.id
 WHERE p.id=$1 AND u.id=$2 FOR UPDATE OF p FOR SHARE OF u`, actor.ProjectID, actor.User.ID).
		Scan(&active, &human, &banned)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return auth.ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if banned {
		return identity.ErrBanned
	}
	if !human {
		return policy.ErrForbidden
	}
	return nil
}

func (s *Reports) insert(ctx context.Context, tx pgx.Tx, actor identity.Actor, targetUser, message, conversation *string, command moderation.ReportCreate) (moderation.Report, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return moderation.Report{}, err
	}
	envelope, err := s.Crypto.SealReport(actor.ProjectID, id.String(), []byte(command.Description))
	if err != nil {
		return moderation.Report{}, err
	}
	row := tx.QueryRow(ctx, `INSERT INTO chat.reports(id,project_id,reporter_id,target_user_id,message_id,reported_message_id,conversation_id,reason,encrypted_description,nonce,key_version,payload_version)
	 VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,$11)
	 RETURNING id::text,reporter_id::text,target_user_id::text,reported_message_id::text,conversation_id::text,reason,encrypted_description,nonce,key_version,payload_version,status,resource_version,reviewed_by::text,reviewed_at,created_at,updated_at`,
		id.String(), actor.ProjectID, actor.User.ID, targetUser, message, conversation, command.Reason,
		envelope.Ciphertext, envelope.Nonce, envelope.KeyVersion, envelope.PayloadVersion)
	reviewed, err := s.scan(actor.ProjectID, row)
	return reviewed.Report, err
}

func (s *Reports) CreateMessage(ctx context.Context, actor identity.Actor, messageID string, command moderation.ReportCreate) (moderation.Report, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.Report{}, err
	}
	defer tx.Rollback(ctx)
	if err = reporterTx(ctx, tx, actor); err != nil {
		return moderation.Report{}, err
	}
	var conversation string
	err = tx.QueryRow(ctx, `SELECT m.conversation_id::text FROM chat.messages m
	JOIN chat.conversation_members cm ON cm.project_id=m.project_id AND cm.conversation_id=m.conversation_id AND cm.user_id=$3
 WHERE m.project_id=$1 AND m.id=$2 AND m.deleted_at IS NULL AND m.expires_at>clock_timestamp()
	 AND cm.left_at IS NULL AND NOT cm.banned FOR SHARE OF m,cm`, actor.ProjectID, messageID, actor.User.ID).Scan(&conversation)
	if errors.Is(err, pgx.ErrNoRows) {
		return moderation.Report{}, identity.ErrNotFound
	}
	if err != nil {
		return moderation.Report{}, err
	}
	result, err := s.insert(ctx, tx, actor, nil, &messageID, &conversation, command)
	if err != nil {
		return moderation.Report{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Reports) CreateUser(ctx context.Context, actor identity.Actor, targetID string, command moderation.ReportCreate) (moderation.Report, error) {
	if targetID == actor.User.ID {
		return moderation.Report{}, policy.ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.Report{}, err
	}
	defer tx.Rollback(ctx)
	if err = reporterTx(ctx, tx, actor); err != nil {
		return moderation.Report{}, err
	}
	var targetExists bool
	err = tx.QueryRow(ctx, "SELECT true FROM chat.users WHERE project_id=$1 AND id=$2 AND kind<>'system' FOR SHARE", actor.ProjectID, targetID).Scan(&targetExists)
	if errors.Is(err, pgx.ErrNoRows) {
		return moderation.Report{}, identity.ErrNotFound
	}
	if err != nil {
		return moderation.Report{}, err
	}
	if command.ConversationID != nil {
		var shared bool
		err = tx.QueryRow(ctx, `SELECT true FROM chat.conversations c
		JOIN chat.conversation_members reporter ON reporter.project_id=c.project_id AND reporter.conversation_id=c.id AND reporter.user_id=$3
	 JOIN chat.conversation_members target ON target.project_id=c.project_id AND target.conversation_id=c.id AND target.user_id=$4
	 WHERE c.project_id=$1 AND c.id=$2 AND c.deleted_at IS NULL AND reporter.left_at IS NULL AND NOT reporter.banned AND target.left_at IS NULL
 FOR SHARE OF c,reporter,target`, actor.ProjectID, *command.ConversationID, actor.User.ID, targetID).Scan(&shared)
		if errors.Is(err, pgx.ErrNoRows) {
			return moderation.Report{}, identity.ErrNotFound
		}
		if err != nil {
			return moderation.Report{}, err
		}
	}
	result, err := s.insert(ctx, tx, actor, &targetID, nil, command.ConversationID, command)
	if err != nil {
		return moderation.Report{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Reports) GetOwn(ctx context.Context, actor identity.Actor, id string) (moderation.Report, error) {
	row := s.DB.QueryRow(ctx, reportProjection+` AND id=$2 AND (reporter_id=$3 OR EXISTS(
 SELECT 1 FROM chat.users u JOIN chat.projects p ON p.id=u.project_id WHERE u.project_id=$1 AND u.id=$3 AND u.role='admin' AND p.status='active'))`, actor.ProjectID, id, actor.User.ID)
	result, err := s.scan(actor.ProjectID, row)
	return result.Report, err
}

func (s *Reports) requireAdminRead(ctx context.Context, actor identity.Actor) error {
	var allowed bool
	err := s.DB.QueryRow(ctx, `SELECT true FROM chat.users u JOIN chat.projects p ON p.id=u.project_id
 WHERE u.project_id=$1 AND u.id=$2 AND u.role='admin' AND p.status='active'`, actor.ProjectID, actor.User.ID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.ErrForbidden
	}
	return err
}

func (s *Reports) List(ctx context.Context, actor identity.Actor, status, after string, limit int) ([]moderation.ReviewedReport, error) {
	if err := s.requireAdminRead(ctx, actor); err != nil {
		return nil, err
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	query := reportProjection + " AND id>$2"
	args := []any{actor.ProjectID, after, limit}
	if status != "" {
		query += " AND status=$4"
		args = append(args, status)
	}
	query += " ORDER BY id LIMIT $3"
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []moderation.ReviewedReport{}
	for rows.Next() {
		item, scanErr := s.scan(actor.ProjectID, rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Reports) GetAdmin(ctx context.Context, actor identity.Actor, id string) (moderation.ReviewedReport, error) {
	if err := s.requireAdminRead(ctx, actor); err != nil {
		return moderation.ReviewedReport{}, err
	}
	return s.scan(actor.ProjectID, s.DB.QueryRow(ctx, reportProjection+" AND id=$2", actor.ProjectID, id))
}

func (s *Reports) Review(ctx context.Context, actor identity.Actor, id string, command moderation.Review, trace policy.Trace) (moderation.ReviewedReport, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.ReviewedReport{}, err
	}
	defer tx.Rollback(ctx)
	if err = adminTx(ctx, tx, actor); err != nil {
		return moderation.ReviewedReport{}, err
	}
	var current string
	var version int64
	err = tx.QueryRow(ctx, "SELECT status,resource_version FROM chat.reports WHERE project_id=$1 AND id=$2 FOR UPDATE", actor.ProjectID, id).Scan(&current, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return moderation.ReviewedReport{}, identity.ErrNotFound
	}
	if err != nil {
		return moderation.ReviewedReport{}, err
	}
	valid := current == "OPEN" && command.Status == "REVIEWING" || current == "REVIEWING" && (command.Status == "RESOLVED" || command.Status == "REJECTED")
	if version != command.ExpectedVersion || !valid {
		return moderation.ReviewedReport{}, policy.ErrConflict
	}
	terminal := command.Status == "RESOLVED" || command.Status == "REJECTED"
	row := tx.QueryRow(ctx, `UPDATE chat.reports SET status=$3,resource_version=resource_version+1,
 reviewed_by=CASE WHEN $4 THEN $5::uuid ELSE NULL END,reviewed_at=CASE WHEN $4 THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp()
	 WHERE project_id=$1 AND id=$2 RETURNING id::text,reporter_id::text,target_user_id::text,reported_message_id::text,conversation_id::text,reason,encrypted_description,nonce,key_version,payload_version,status,resource_version,reviewed_by::text,reviewed_at,created_at,updated_at`,
		actor.ProjectID, id, command.Status, terminal, actor.User.ID)
	result, err := s.scan(actor.ProjectID, row)
	if err != nil {
		return moderation.ReviewedReport{}, err
	}
	if err = audit(ctx, tx, actor, "report."+strings.ToLower(command.Status), "report", id, trace, map[string]any{"from_status": current, "to_status": command.Status}); err != nil {
		return moderation.ReviewedReport{}, err
	}
	return result, tx.Commit(ctx)
}
