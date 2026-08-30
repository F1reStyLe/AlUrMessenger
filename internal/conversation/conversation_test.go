package conversation

import (
	"testing"

	"github.com/google/uuid"
)

// TestCreateValidation covers bounded membership, DIRECT invariants and metadata hygiene.
func TestCreateValidation(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	valid := []Create{{Type: "DIRECT", MemberIDs: []string{b}}, {Type: "GROUP"}, {Type: "CHANNEL", MemberIDs: []string{b}, Title: "Updates", AvatarURL: "https://cdn.example/avatar.png"}}
	for _, p := range valid {
		if err := p.Validate(a); err != nil {
			t.Fatal("valid create rejected", err)
		}
	}
	invalid := []Create{
		{}, {Type: "direct", MemberIDs: []string{b}}, {Type: "DIRECT"}, {Type: "DIRECT", MemberIDs: []string{a}},
		{Type: "DIRECT", MemberIDs: []string{b}, Title: "named"}, {Type: "GROUP", MemberIDs: []string{b, b}},
		{Type: "CHANNEL", Title: " padded "}, {Type: "GROUP", AvatarURL: "http://insecure.example/a.png"},
	}
	for _, p := range invalid {
		if err := p.Validate(a); err == nil {
			t.Fatal("invalid create accepted", p)
		}
	}
}

// TestPatchValidation protects optimistic concurrency and immutable identifiers.
func TestPatchValidation(t *testing.T) {
	title, avatar := "Team", "https://cdn.example/a.png"
	for _, p := range []Patch{{Title: &title, ExpectedVersion: 1}, {AvatarURL: &avatar, ExpectedVersion: 2}} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	badTitle, badAvatar := "\n", "file:///secret"
	for _, p := range []Patch{{ExpectedVersion: 1}, {Title: &title}, {Title: &badTitle, ExpectedVersion: 1}, {AvatarURL: &badAvatar, ExpectedVersion: 1}} {
		if err := p.Validate(); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
}
