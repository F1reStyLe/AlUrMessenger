package provision

import (
	"context"
	"errors"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// seedConversations adds stable development conversations once, without restoring
// members or moderators on repeat. It is called only after Seed's development guard.
func seedConversations(ctx context.Context, db *pgx.Conn) error {
	users := map[string]string{}
	for _, name := range []string{"admin", "alice", "bob"} {
		var id string
		if err := db.QueryRow(ctx, "SELECT id::text FROM chat.users WHERE project_id=$1 AND external_user_id=$2", DevProject, name).Scan(&id); err != nil {
			return err
		}
		users[name] = id
	}
	for i, kind := range []string{"DIRECT", "GROUP", "CHANNEL"} {
		id := []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102", "00000000-0000-4000-8000-000000000103"}[i]
		if err := seedConversation(ctx, db, id, kind, users); err != nil {
			return err
		}
	}
	return nil
}

// seedConversation uses an explicit stable fixture ID and a transaction lock so
// concurrent seed runs do not duplicate data or overwrite an edited conversation.
func seedConversation(ctx context.Context, db *pgx.Conn, id, kind string, users map[string]string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "dev.seed:"+id); err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRow(ctx, "SELECT project_id=$2 AND type=$3 FROM chat.conversations WHERE id=$1", id, DevProject, kind).Scan(&matches)
	if err == nil {
		if !matches {
			return errors.New("DEV_CONVERSATION_CONFLICT")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	creator, title := users["admin"], "Development "+kind
	members := []string{users["admin"], users["alice"], users["bob"]}
	if kind == "DIRECT" {
		creator = users["alice"]
		title = ""
		members = members[1:]
		sort.Strings(members)
		// A real request may have created this pair before the first seed. Reuse
		// it instead of competing with the canonical pair constraint.
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "chat.direct:"+DevProject+":"+members[0]+":"+members[1]); err != nil {
			return err
		}
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.direct_pairs WHERE project_id=$1 AND low_user_id=$2 AND high_user_id=$3)", DevProject, members[0], members[1]).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return tx.Commit(ctx)
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.conversations(id,project_id,type,title,created_by) VALUES($1,$2,$3,$4,$5)", id, DevProject, kind, title, creator); err != nil {
		return err
	}
	if kind == "DIRECT" {
		if _, err = tx.Exec(ctx, "INSERT INTO chat.direct_pairs(project_id,low_user_id,high_user_id,conversation_id) VALUES($1,$2,$3,$4)", DevProject, members[0], members[1], id); err != nil {
			return err
		}
	}
	for _, user := range members {
		role := "member"
		if user == creator && kind != "DIRECT" {
			role = "moderator"
		}
		if _, err = tx.Exec(ctx, "INSERT INTO chat.conversation_members(project_id,conversation_id,user_id,role) VALUES($1,$2,$3,$4)", DevProject, id, user, role); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.audit_logs(id,project_id,actor_id,actor_type,action,resource_type,resource_id,request_id) VALUES($1,$2,$3,'user','conversation.created','conversation',$4,$5)", uuid.NewString(), DevProject, creator, id, uuid.NewString()); err != nil {
		return err
	}
	if _, err = eventrepo.Append(ctx, tx, DevProject, id, "conversation.created", map[string]any{"type": kind, "member_count": len(members)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
