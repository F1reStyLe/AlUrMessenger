//go:build integration

package integration

import (
	"sync"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testConversations uses runtime credentials for all product writes. Operator SQL
// only prepares unique fixture identities and tries attacks that DB constraints must reject.
func testConversations(t *testing.T, db *pgxpool.Pool, operator *pgx.Conn, redisURL string) {
	p := provision.Project{ID: uuid.NewString(), Name: "Conversations", Issuer: "https://issuer.test", Audience: "chat"}
	q := p
	q.ID = uuid.NewString()
	q.Name = "Other Project"
	for _, project := range []provision.Project{p, q} {
		if err := provision.Create(t.Context(), operator, project); err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(project string) identity.User {
		u := identity.User{ID: uuid.NewString(), ExternalID: uuid.NewString(), Kind: "human", Role: "user"}
		_, err := operator.Exec(t.Context(), "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1,$2,$3,$3)", u.ID, project, u.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	a, b, outsider, foreign := newUser(p.ID), newUser(p.ID), newUser(p.ID), newUser(q.ID)
	actor := identity.Actor{User: a}
	actor.ProjectID = p.ID
	otherActor := identity.Actor{User: b}
	otherActor.ProjectID = p.ID
	outsiderActor := identity.Actor{User: outsider}
	outsiderActor.ProjectID = p.ID
	foreignActor := identity.Actor{User: foreign}
	foreignActor.ProjectID = q.ID
	s := &conversation.Service{Store: &conversationrepo.Store{DB: db}}
	payload := conversation.Create{Type: "DIRECT", MemberIDs: []string{b.ID}}
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	createdFlags := make(chan bool, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, n, e := s.Create(t.Context(), actor, payload, policy.Trace{RequestID: uuid.NewString()})
			ids <- c.ID
			createdFlags <- n
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(createdFlags)
	close(errs)
	var id string
	createdCount := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for candidate := range ids {
		if id != "" && id != candidate {
			t.Fatal("duplicate DIRECT")
		}
		id = candidate
	}
	for v := range createdFlags {
		if v {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatal("DIRECT created count", createdCount)
	}
	c, err := s.Get(t.Context(), otherActor, id)
	if err != nil || c.ID != id || c.Membership.Role != "member" {
		t.Fatal("partner cannot read", err)
	}
	for _, blocked := range []identity.Actor{outsiderActor, foreignActor} {
		if _, err = s.Get(t.Context(), blocked, id); err != identity.ErrNotFound {
			t.Fatal("private/cross-project conversation visible", err)
		}
	}
	if _, err = operator.Exec(t.Context(), "INSERT INTO chat.conversation_members(project_id,conversation_id,user_id,role) VALUES($1,$2,$3,'member')", p.ID, id, outsider.ID); err == nil {
		t.Fatal("third DIRECT member accepted")
	}
	title := "forbidden"
	if _, err = s.Patch(t.Context(), actor, id, conversation.Patch{Title: &title, ExpectedVersion: 1}, policy.Trace{RequestID: uuid.NewString()}); err != conversation.ErrUnsupported {
		t.Fatal("DIRECT metadata changed", err)
	}
	group, created, err := s.Create(t.Context(), actor, conversation.Create{Type: "GROUP", MemberIDs: []string{b.ID}, Title: "Team"}, policy.Trace{RequestID: uuid.NewString()})
	if err != nil || !created || group.Membership.Role != "moderator" {
		t.Fatal("group creator is not moderator", err)
	}
	updated := "Team 2"
	group, err = s.Patch(t.Context(), actor, group.ID, conversation.Patch{Title: &updated, ExpectedVersion: 1}, policy.Trace{RequestID: uuid.NewString()})
	if err != nil || group.Title != updated || group.Version != 2 {
		t.Fatal("moderator patch failed", err)
	}
	if _, err = s.Patch(t.Context(), otherActor, group.ID, conversation.Patch{Title: &title, ExpectedVersion: 2}, policy.Trace{RequestID: uuid.NewString()}); err != policy.ErrForbidden {
		t.Fatal("member changed metadata", err)
	}
	if _, err = s.Patch(t.Context(), actor, group.ID, conversation.Patch{Title: &title, ExpectedVersion: 1}, policy.Trace{RequestID: uuid.NewString()}); err != policy.ErrConflict {
		t.Fatal("stale version accepted", err)
	}
	items, err := s.List(t.Context(), actor, "", 101)
	if err != nil || len(items) != 2 {
		t.Fatal("membership list invalid", len(items), err)
	}
	otherItems, err := s.List(t.Context(), outsiderActor, "", 101)
	if err != nil || len(otherItems) != 0 {
		t.Fatal("outsider list leaked", len(otherItems), err)
	}
	var auditCount int
	if err = db.QueryRow(t.Context(), "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND resource_type='conversation'", p.ID).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatal("conversation audit not atomic", auditCount, err)
	}
	// Initial creation and follow-up membership management use the same aggregate
	// lock; global admin is never a substitute for membership/moderator authority.
	ms := &conversation.MembershipService{Store: &conversationrepo.Store{DB: db}}
	trace := func() policy.Trace { return policy.Trace{RequestID: uuid.NewString()} }
	memberRole, moderatorRole := "member", "moderator"
	if err = ms.Remove(t.Context(), actor, group.ID, a.ID, trace()); err != conversation.ErrLastModerator {
		t.Fatal("last moderator left", err)
	}
	if _, err = ms.Patch(t.Context(), actor, group.ID, a.ID, conversation.MemberPatch{Role: &memberRole, ExpectedVersion: 1}, trace()); err != conversation.ErrLastModerator {
		t.Fatal("last moderator demoted", err)
	}
	if _, err = ms.Add(t.Context(), otherActor, group.ID, conversation.AddMembers{UserIDs: []string{outsider.ID}}, trace()); err != policy.ErrForbidden {
		t.Fatal("member invited others", err)
	}
	if _, err = ms.Add(t.Context(), actor, group.ID, conversation.AddMembers{UserIDs: []string{outsider.ID}}, trace()); err != nil {
		t.Fatal(err)
	}
	mute := true
	if _, err = ms.Patch(t.Context(), otherActor, group.ID, b.ID, conversation.MemberPatch{Muted: &mute, ExpectedVersion: 1}, trace()); err != nil {
		t.Fatal("self mute failed", err)
	}
	if _, err = ms.Patch(t.Context(), otherActor, group.ID, a.ID, conversation.MemberPatch{Muted: &mute, ExpectedVersion: 1}, trace()); err != policy.ErrForbidden {
		t.Fatal("other mute allowed", err)
	}
	if err = ms.Remove(t.Context(), otherActor, group.ID, b.ID, trace()); err != nil {
		t.Fatal("member leave failed", err)
	}
	if _, err = s.Get(t.Context(), otherActor, group.ID); err != identity.ErrNotFound {
		t.Fatal("left user reads group", err)
	}
	if _, err = ms.Members(t.Context(), otherActor, group.ID, "", 100); err != identity.ErrNotFound {
		t.Fatal("left user reads members", err)
	}
	rejoined, err := ms.Add(t.Context(), actor, group.ID, conversation.AddMembers{UserIDs: []string{b.ID}}, trace())
	if err != nil || len(rejoined) != 1 || rejoined[0].Role != "member" {
		t.Fatal("rejoin failed", err)
	}
	ban := true
	blocked, err := ms.Patch(t.Context(), actor, group.ID, b.ID, conversation.MemberPatch{Banned: &ban, ExpectedVersion: rejoined[0].Version}, trace())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(t.Context(), otherActor, group.ID); err != nil {
		t.Fatal("banned member lost read", err)
	}
	if _, err = ms.Patch(t.Context(), otherActor, group.ID, b.ID, conversation.MemberPatch{Muted: &mute, ExpectedVersion: blocked.Version}, trace()); err != identity.ErrBanned {
		t.Fatal("banned member wrote", err)
	}
	unban := false
	unblocked, err := ms.Patch(t.Context(), actor, group.ID, b.ID, conversation.MemberPatch{Banned: &unban, Role: &moderatorRole, ExpectedVersion: blocked.Version}, trace())
	if err != nil {
		t.Fatal(err)
	}
	outsiderActor.Admin = true
	if _, err = s.Patch(t.Context(), outsiderActor, group.ID, conversation.Patch{Title: &title, ExpectedVersion: 2}, trace()); err != policy.ErrForbidden {
		t.Fatal("Project admin bypassed moderator", err)
	}
	// Concurrent self-demotions must leave exactly one active moderator.
	results := make(chan error, 2)
	for _, entry := range []struct {
		a       identity.Actor
		version int64
	}{{actor, 1}, {otherActor, unblocked.Version}} {
		go func(a identity.Actor, version int64) {
			_, e := ms.Patch(t.Context(), a, group.ID, a.User.ID, conversation.MemberPatch{Role: &memberRole, ExpectedVersion: version}, trace())
			results <- e
		}(entry.a, entry.version)
	}
	success, guarded := 0, 0
	for range 2 {
		e := <-results
		if e == nil {
			success++
		} else if e == conversation.ErrLastModerator {
			guarded++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || guarded != 1 {
		t.Fatal("last moderator race", success, guarded)
	}
	if err = ms.Remove(t.Context(), actor, id, a.ID, trace()); err != conversation.ErrUnsupported {
		t.Fatal("DIRECT leave allowed", err)
	}
	if _, err = ms.Patch(t.Context(), actor, id, a.ID, conversation.MemberPatch{Muted: &mute, ExpectedVersion: 1}, trace()); err != nil {
		t.Fatal("DIRECT mute denied", err)
	}
	testConversationHTTP(t, db, redisURL, []identity.Actor{actor, otherActor, outsiderActor, foreignActor})
}
