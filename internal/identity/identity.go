// Package identity задаёт Project-scoped профили и typed actor, не завися от HTTP.
package identity

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
)

// User — публичный профиль; external ID выдаётся только отдельным self DTO.
type User struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	AvatarURL   string     `json:"avatar_url"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	ExternalID  string     `json:"-"`
	Banned      bool       `json:"-"`
}

// Actor связывает проверенный JWT и внутренний профиль одного Project.
type Actor struct {
	auth.Identity
	User User
}

// Domain errors отделяют публичные отказы от ошибок драйвера/SQL.
var ErrNotFound = errors.New("RESOURCE_NOT_FOUND")
var ErrBanned = errors.New("USER_BANNED")

// ProfilePatch содержит только редактируемые поля; actor/project/user IDs отсутствуют.
type ProfilePatch struct {
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

// Validate ограничивает имя и внешний avatar URL без скачивания ресурса сервером.
func (p ProfilePatch) Validate() error {
	if p.DisplayName == nil && p.AvatarURL == nil {
		return errors.New("INVALID_REQUEST")
	}
	if p.DisplayName != nil {
		v := *p.DisplayName
		if strings.TrimSpace(v) != v || v == "" || utf8.RuneCountInString(v) > 128 || strings.IndexFunc(v, unicode.IsControl) != -1 {
			return errors.New("INVALID_REQUEST")
		}
	}
	if p.AvatarURL != nil && *p.AvatarURL != "" {
		u, err := url.Parse(*p.AvatarURL)
		if err != nil || len(*p.AvatarURL) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return errors.New("INVALID_REQUEST")
		}
	}
	return nil
}

// Store делает tenancy обязательной частью каждого repository method.
type Store interface {
	EnsureUser(context.Context, auth.Identity) (User, error)
	GetUser(context.Context, string, string) (User, error)
	ListUsers(context.Context, string, string, int) ([]User, error)
	PatchUser(context.Context, Actor, ProfilePatch) (User, error)
}

// Service аутентифицирует до auto-provision: невалидный JWT не создаёт строки.
type Service struct {
	Verifier auth.Verifier
	Store    Store
}

// Authenticate не принимает внутренний user ID от клиента; он разрешается через JWT sub.
func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	id, err := s.Verifier.Verify(ctx, token)
	if err != nil {
		return Actor{}, err
	}
	user, err := s.Store.EnsureUser(ctx, id)
	if err != nil {
		return Actor{}, err
	}
	return Actor{Identity: id, User: user}, nil
}
