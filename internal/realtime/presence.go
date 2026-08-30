// Package realtime owns WebSocket connections and ephemeral Redis state. PostgreSQL
// remains authoritative for membership, checkpoints and replay; Redis stores no content.
package realtime

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Presence tracks a bounded set of devices per user, using server-side Redis time.
type Presence struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

const seenKey = "alur:last-seen:pending"

func connectionKey(a identity.Actor) string {
	return "alur:connections:" + a.ProjectID + ":" + a.User.ID
}

var heartbeat = redis.NewScript(`local t=redis.call('TIME');local now=tonumber(t[1]);redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now);if redis.call('ZSCORE',KEYS[1],ARGV[1])==false and redis.call('ZCARD',KEYS[1])>=16 then return 0 end;redis.call('ZADD',KEYS[1],now+75,ARGV[1]);redis.call('EXPIRE',KEYS[1],150);redis.call('SET',KEYS[2],ARGV[2],'EX',75);redis.call('ZADD',KEYS[3],now,ARGV[3]);return 1`)

// Touch is an atomic lease renewal. The device ID is metadata, never authorization.
func (p *Presence) Touch(ctx context.Context, a identity.Actor, id, device string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	n, err := heartbeat.Run(ctx, p.Redis, []string{connectionKey(a), "alur:connection:" + id, seenKey}, id, device, a.ProjectID+":"+a.User.ID).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("CONNECTION_LIMIT")
	}
	return nil
}

// Drop removes only this connection; other devices remain online until their own TTL.
func (p *Presence) Drop(ctx context.Context, a identity.Actor, id string) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pipe := p.Redis.TxPipeline()
	pipe.ZRem(ctx, connectionKey(a), id)
	pipe.Del(ctx, "alur:connection:"+id)
	pipe.Exec(ctx)
}

// Status never fabricates offline during a Redis outage, or reveals nonmembers.
type Status struct {
	UserID   string     `json:"user_id"`
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"last_seen_at"`
}

func (p *Presence) Statuses(ctx context.Context, a identity.Actor, id string, users []string) ([]Status, error) {
	if len(users) > 100 || !conversation.ValidID(id) {
		return nil, policy.ErrInvalid
	}
	for _, user := range users {
		if !conversation.ValidID(user) {
			return nil, policy.ErrInvalid
		}
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = p.access(ctx, tx, a, id, "presence_enabled", false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT u.id::text,u.last_seen_at FROM chat.users u JOIN chat.conversation_members m ON m.project_id=u.project_id AND m.user_id=u.id WHERE u.project_id=$1 AND m.conversation_id=$2 AND m.left_at IS NULL AND u.id=ANY($3::uuid[]) ORDER BY u.id`, a.ProjectID, id, users)
	if err != nil {
		return nil, err
	}
	result := []Status{}
	for rows.Next() {
		var s Status
		if err = rows.Scan(&s.UserID, &s.LastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	clock, err := p.Redis.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	for i := range result {
		other := a
		other.User.ID = result[i].UserID
		count, e := p.Redis.ZCount(ctx, connectionKey(other), "("+strconv.FormatInt(clock.Unix(), 10), "+inf").Result()
		if e != nil {
			return nil, e
		}
		result[i].Online = count > 0
	}
	return result, tx.Commit(ctx)
}

// access locks current membership while the ephemeral operation runs. A disabled
// feature is explicit, so cached presence must be cleared instead of read as offline.
func (p *Presence) access(ctx context.Context, tx pgx.Tx, a identity.Actor, id, flag string, write bool) error {
	var enabled, banned, projectBan bool
	var role, kind string
	err := tx.QueryRow(ctx, `SELECT COALESCE((s.flags->>$4)::boolean,false),m.banned,u.banned_at IS NOT NULL,m.role,c.type
 FROM chat.projects p JOIN chat.project_settings s ON s.project_id=p.id JOIN chat.conversations c ON c.project_id=p.id
 JOIN chat.conversation_members m ON m.project_id=c.project_id AND m.conversation_id=c.id JOIN chat.users u ON u.project_id=m.project_id AND u.id=m.user_id
 WHERE p.id=$1 AND p.status='active' AND c.id=$2 AND c.deleted_at IS NULL AND m.user_id=$3 AND m.left_at IS NULL FOR SHARE OF p,u,m`, a.ProjectID, id, a.User.ID, flag).Scan(&enabled, &banned, &projectBan, &role, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return policy.ErrFeatureDisabled
	}
	if write {
		if banned || projectBan {
			return identity.ErrBanned
		}
		if kind == "CHANNEL" && role != "moderator" {
			return policy.ErrForbidden
		}
	}
	return nil
}

// Typing is a per-connection lease; stopping one tab cannot stop another tab's lease.
func (p *Presence) Typing(ctx context.Context, a identity.Actor, id, connection string, start bool) error {
	if !conversation.ValidID(id) {
		return policy.ErrInvalid
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = p.access(ctx, tx, a, id, "typing_enabled", true); err != nil {
		return err
	}
	key := "alur:typing:" + a.ProjectID + ":" + id
	member := a.User.ID + ":" + connection
	if start {
		clock, e := p.Redis.Time(ctx).Result()
		if e != nil {
			return e
		}
		pipe := p.Redis.TxPipeline()
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(clock.Unix(), 10))
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(clock.Unix() + 5), Member: member})
		pipe.Expire(ctx, key, 10*time.Second)
		_, err = pipe.Exec(ctx)
	} else {
		err = p.Redis.ZRem(ctx, key, member).Err()
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Typers filters stale/departed/banned senders against current SQL before exposing IDs.
func (p *Presence) Typers(ctx context.Context, a identity.Actor, id string) ([]string, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = p.access(ctx, tx, a, id, "typing_enabled", false); err != nil {
		return nil, err
	}
	clock, err := p.Redis.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	entries, err := p.Redis.ZRangeByScore(ctx, "alur:typing:"+a.ProjectID+":"+id, &redis.ZRangeBy{Min: "(" + strconv.FormatInt(clock.Unix(), 10), Max: "+inf", Count: 1600}).Result()
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	ids := []string{}
	for _, entry := range entries {
		user, _, ok := strings.Cut(entry, ":")
		if ok && conversation.ValidID(user) && !unique[user] {
			unique[user] = true
			ids = append(ids, user)
		}
	}
	rows, err := tx.Query(ctx, `SELECT m.user_id::text FROM chat.conversation_members m JOIN chat.users u ON u.project_id=m.project_id AND u.id=m.user_id JOIN chat.conversations c ON c.project_id=m.project_id AND c.id=m.conversation_id WHERE m.project_id=$1 AND m.conversation_id=$2 AND m.user_id=ANY($3::uuid[]) AND m.left_at IS NULL AND NOT m.banned AND u.banned_at IS NULL AND (c.type<>'CHANNEL' OR m.role='moderator') ORDER BY m.user_id`, a.ProjectID, id, ids)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

var removeSeen = redis.NewScript(`if redis.call('ZSCORE',KEYS[1],ARGV[1])==ARGV[2] then return redis.call('ZREM',KEYS[1],ARGV[1]) end;return 0`)

// Flush writes one bounded batch and removes a marker only if no newer heartbeat
// replaced it. Concurrent workers are safe because SQL applies GREATEST.
func (p *Presence) Flush(ctx context.Context) error {
	entries, err := p.Redis.ZRangeWithScores(ctx, seenKey, 0, 255).Result()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		member, ok := entry.Member.(string)
		if !ok {
			continue
		}
		project, user, ok := strings.Cut(member, ":")
		if !ok || !conversation.ValidID(project) || !conversation.ValidID(user) {
			continue
		}
		if _, err = p.DB.Exec(ctx, "UPDATE chat.users SET last_seen_at=GREATEST(last_seen_at,to_timestamp($3)) WHERE project_id=$1 AND id=$2", project, user, entry.Score); err != nil {
			return err
		}
		if err = removeSeen.Run(ctx, p.Redis, []string{seenKey}, member, strconv.FormatFloat(entry.Score, 'f', -1, 64)).Err(); err != nil {
			return err
		}
	}
	return nil
}

// RunFlush persists last-seen every 30s, avoiding a PostgreSQL write per heartbeat.
func (p *Presence) RunFlush(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			p.Flush(bounded)
			cancel()
		}
	}
}
