package repository

import (
	"context"
	"errors"
	"sort"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// memberColumns is never selected without Project and conversation predicates.
const memberColumns = "user_id::text,role,joined_at,banned,version,left_at,muted"

// scanMember hides absent/cross-Project targets with the same 404 response.
func scanMember(row pgx.Row) (conversation.Member, error) {
	var m conversation.Member
	err := row.Scan(&m.UserID, &m.Role, &m.JoinedAt, &m.Banned, &m.Version, &m.LeftAt, &m.Muted)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrNotFound
	}
	return m, err
}

// Members holds the caller's membership lock through page selection, so a concurrent
// leave cannot commit between the permission check and the member query.
func (s *Store) Members(ctx context.Context, a identity.Actor, id, after string, limit int) ([]conversation.Member, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = get(ctx, tx, a, id, true); err != nil {
		return nil, err
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := tx.Query(ctx, "SELECT "+memberColumns+" FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND left_at IS NULL AND user_id>$3 ORDER BY user_id LIMIT $4", a.ProjectID, id, after, limit)
	if err != nil {
		return nil, err
	}
	items := []conversation.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}

// membershipTx serializes all role/leave/ban decisions on the aggregate row.
// Sorted users precede conversation/member locks, matching creation and metadata patch.
func (s *Store) membershipTx(ctx context.Context, a identity.Actor, id string, others []string, inviting bool) (pgx.Tx, conversation.Conversation, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, conversation.Conversation{}, err
	}
	fail := func(err error) (pgx.Tx, conversation.Conversation, error) {
		tx.Rollback(ctx)
		return nil, conversation.Conversation{}, err
	}
	if err = lockProject(ctx, tx, a.ProjectID); err != nil {
		return fail(err)
	}
	unique := map[string]bool{a.User.ID: true}
	for _, user := range others {
		unique[user] = true
	}
	ids := []string{}
	for user := range unique {
		ids = append(ids, user)
	}
	sort.Strings(ids)
	if err = lockUsers(ctx, tx, a, ids, inviting); err != nil {
		return fail(err)
	}
	var key string
	err = tx.QueryRow(ctx, "SELECT id::text FROM chat.conversations WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE", a.ProjectID, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrNotFound
	}
	if err != nil {
		return fail(err)
	}
	c, err := get(ctx, tx, a, id, true)
	if err != nil {
		return fail(err)
	}
	if c.Membership.Banned {
		return fail(identity.ErrBanned)
	}
	return tx, c, nil
}

// AddMembers is atomic for the batch. Existing active memberships retain their
// role/ban/mute; a previously left user rejoins as an ordinary member, never moderator.
func (s *Store) AddMembers(ctx context.Context, a identity.Actor, id string, p conversation.AddMembers, tr policy.Trace) ([]conversation.Member, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	tx, c, err := s.membershipTx(ctx, a, id, p.UserIDs, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if c.Type == "DIRECT" {
		return nil, conversation.ErrUnsupported
	}
	if c.Membership.Role != "moderator" {
		return nil, policy.ErrForbidden
	}
	items := []conversation.Member{}
	for _, user := range p.UserIDs {
		tag, execErr := tx.Exec(ctx, `INSERT INTO chat.conversation_members(project_id,conversation_id,user_id,role) VALUES($1,$2,$3,'member')
 ON CONFLICT(project_id,conversation_id,user_id) DO UPDATE SET role='member',left_at=NULL,banned=false,version=chat.conversation_members.version+1 WHERE chat.conversation_members.left_at IS NOT NULL`, a.ProjectID, id, user)
		if execErr != nil {
			return nil, execErr
		}
		if tag.RowsAffected() > 0 {
			if err = audit(ctx, tx, a, id, "conversation.members.added", map[string]any{"user_ids": []string{user}}, tr); err != nil {
				return nil, err
			}
		}
		m, err := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3", a.ProjectID, id, user))
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, tx.Commit(ctx)
}

// keepModerator requires a different active, non-banned moderator before removing
// the target's authority. Conversation FOR UPDATE prevents two simultaneous removals.
func keepModerator(ctx context.Context, tx pgx.Tx, project, id, user string) error {
	var remaining bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id<>$3 AND role='moderator' AND left_at IS NULL AND NOT banned)", project, id, user).Scan(&remaining)
	if err != nil {
		return err
	}
	if !remaining {
		return conversation.ErrLastModerator
	}
	return nil
}

// PatchMember separates self-only mute from moderator-only role/ban operations.
func (s *Store) PatchMember(ctx context.Context, a identity.Actor, id, user string, p conversation.MemberPatch, tr policy.Trace) (conversation.Member, error) {
	if err := p.Validate(); err != nil {
		return conversation.Member{}, err
	}
	tx, c, err := s.membershipTx(ctx, a, id, []string{user}, false)
	if err != nil {
		return conversation.Member{}, err
	}
	defer tx.Rollback(ctx)
	if c.Type == "DIRECT" && (p.Role != nil || p.Banned != nil) {
		return conversation.Member{}, conversation.ErrUnsupported
	}
	if (p.Muted != nil && user != a.User.ID) || ((p.Role != nil || p.Banned != nil) && c.Membership.Role != "moderator") {
		return conversation.Member{}, policy.ErrForbidden
	}
	m, err := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 AND left_at IS NULL FOR UPDATE", a.ProjectID, id, user))
	if err != nil {
		return m, err
	}
	if m.Version != p.ExpectedVersion {
		return m, policy.ErrConflict
	}
	// Only human actors can manage memberships in the current API. Do not allow
	// a bot to become the sole moderator without a supported management identity.
	if p.Role != nil && *p.Role == "moderator" {
		var human bool
		if err = tx.QueryRow(ctx, "SELECT kind='human' FROM chat.users WHERE project_id=$1 AND id=$2", a.ProjectID, user).Scan(&human); err != nil {
			return m, err
		}
		if !human {
			return m, policy.ErrForbidden
		}
	}
	if m.Role == "moderator" && ((p.Role != nil && *p.Role != "moderator") || (p.Banned != nil && *p.Banned)) {
		if err = keepModerator(ctx, tx, a.ProjectID, id, user); err != nil {
			return m, err
		}
	}
	m, err = scanMember(tx.QueryRow(ctx, "UPDATE chat.conversation_members SET role=COALESCE($4,role),banned=COALESCE($5,banned),muted=COALESCE($6,muted),version=version+1 WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 RETURNING "+memberColumns, a.ProjectID, id, user, p.Role, p.Banned, p.Muted))
	if err != nil {
		return m, err
	}
	if err = audit(ctx, tx, a, id, "conversation.member.updated", map[string]any{"user_id": user, "role_changed": p.Role != nil, "ban_changed": p.Banned != nil, "mute_changed": p.Muted != nil}, tr); err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

// RemoveMember records leave rather than deleting identity/history. A nonmember
// cannot use this command to inspect or alter another user's private membership.
func (s *Store) RemoveMember(ctx context.Context, a identity.Actor, id, user string, tr policy.Trace) error {
	tx, c, err := s.membershipTx(ctx, a, id, []string{user}, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if c.Type == "DIRECT" {
		return conversation.ErrUnsupported
	}
	if user != a.User.ID && c.Membership.Role != "moderator" {
		return policy.ErrForbidden
	}
	m, err := scanMember(tx.QueryRow(ctx, "SELECT "+memberColumns+" FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 AND left_at IS NULL FOR UPDATE", a.ProjectID, id, user))
	if err != nil {
		return err
	}
	if m.Role == "moderator" {
		if err = keepModerator(ctx, tx, a.ProjectID, id, user); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.conversation_members SET left_at=clock_timestamp(),version=version+1 WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3", a.ProjectID, id, user); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, id, "conversation.member.left", map[string]any{"user_id": user}, tr); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
