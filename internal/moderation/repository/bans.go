package repository

import (
	"context"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Bans struct{ DB *pgxpool.Pool }

func scanBan(row pgx.Row) (moderation.BanState, error) {
	var result moderation.BanState
	err := row.Scan(&result.UserID, &result.Banned, &result.Reason, &result.Version, &result.BannedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, identity.ErrNotFound
	}
	return result, err
}

const banProjection = "id::text,banned_at IS NOT NULL,ban_reason,policy_version,banned_at"

func (s *Bans) change(ctx context.Context, actor identity.Actor, user string, expected int64, reason *string, trace policy.Trace) (moderation.BanState, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.BanState{}, err
	}
	defer tx.Rollback(ctx)
	if err = adminTx(ctx, tx, actor); err != nil {
		return moderation.BanState{}, err
	}
	var kind, role, oldReason string
	var version int64
	var banned bool
	err = tx.QueryRow(ctx, `SELECT kind,role,banned_at IS NOT NULL,ban_reason,policy_version FROM chat.users
 WHERE project_id=$1 AND id=$2 FOR UPDATE`, actor.ProjectID, user).Scan(&kind, &role, &banned, &oldReason, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return moderation.BanState{}, identity.ErrNotFound
	}
	if err != nil {
		return moderation.BanState{}, err
	}
	if kind == "system" {
		return moderation.BanState{}, policy.ErrForbidden
	}
	if version != expected {
		return moderation.BanState{}, policy.ErrConflict
	}
	wantBan := reason != nil
	newReason := ""
	if reason != nil {
		newReason = *reason
	}
	if banned == wantBan {
		// PUT/DELETE retries with the current version are true no-ops. A ban reason
		// is immutable until unban/re-ban, avoiding a hidden second operation.
		return scanBan(tx.QueryRow(ctx, "SELECT "+banProjection+" FROM chat.users WHERE project_id=$1 AND id=$2", actor.ProjectID, user))
	}
	if wantBan && role == "admin" {
		var another bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.users WHERE project_id=$1 AND id<>$2 AND role='admin' AND banned_at IS NULL)", actor.ProjectID, user).Scan(&another); err != nil {
			return moderation.BanState{}, err
		}
		if !another {
			return moderation.BanState{}, policy.ErrConflict
		}
	}
	action := "user.unbanned"
	if wantBan {
		action = "user.banned"
	}
	result, err := scanBan(tx.QueryRow(ctx, `UPDATE chat.users SET banned_at=CASE WHEN $3 THEN clock_timestamp() ELSE NULL END,
 ban_reason=CASE WHEN $3 THEN $4 ELSE '' END,policy_version=policy_version+1
	 WHERE project_id=$1 AND id=$2 RETURNING `+banProjection, actor.ProjectID, user, wantBan, newReason))
	if err != nil {
		return moderation.BanState{}, err
	}
	if err = audit(ctx, tx, actor, action, "user", user, trace, map[string]any{"changed_fields": []string{"banned"}}); err != nil {
		return moderation.BanState{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Bans) Ban(ctx context.Context, actor identity.Actor, user string, command moderation.BanCommand, trace policy.Trace) (moderation.BanState, error) {
	return s.change(ctx, actor, user, command.ExpectedVersion, &command.Reason, trace)
}

func (s *Bans) Unban(ctx context.Context, actor identity.Actor, user string, command moderation.UnbanCommand, trace policy.Trace) (moderation.BanState, error) {
	return s.change(ctx, actor, user, command.ExpectedVersion, nil, trace)
}
