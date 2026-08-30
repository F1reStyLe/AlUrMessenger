// Package transport связывает HTTP DTO с identity application service.
package transport

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/google/uuid"
)

// actorKey нельзя подделать через HTTP headers или context key другого пакета.
type actorKey struct{}

// Actor используется только внутри защищённых маршрутов, после Authenticate.
func Actor(ctx context.Context) identity.Actor {
	a, _ := ctx.Value(actorKey{}).(identity.Actor)
	return a
}

// Authenticate принимает ровно один Bearer header. Query/cookie JWT не поддерживаются.
// Deadline ограничивает DB lookup и provisioning независимо от socket timeout.
func Authenticate(service *identity.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		headers := r.Header.Values("Authorization")
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(headers) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			Failure(w, r, auth.ErrUnauthenticated)
			return
		}
		a, err := service.Authenticate(ctx, parts[1])
		if err != nil {
			Failure(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, actorKey{}, a)))
	})
}

// Methods сохраняет единый JSON 405 вместо текстового ответа ServeMux.
func Methods(allowed string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, method := range strings.Split(allowed, ", ") {
			if r.Method == method {
				next(w, r)
				return
			}
		}
		w.Header().Set("Allow", allowed)
		httpserver.WriteError(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	})
}

// Failure не сериализует SQL/crypto errors; неожиданный storage failure становится 503.
func Failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpserver.WriteError(w, r, 401, "UNAUTHENTICATED", "Valid access token required")
	case errors.Is(err, identity.ErrNotFound):
		httpserver.WriteError(w, r, 404, "RESOURCE_NOT_FOUND", "Resource not found")
	case errors.Is(err, identity.ErrBanned):
		httpserver.WriteError(w, r, 403, "USER_BANNED", "User is read-only")
	default:
		httpserver.WriteError(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Operation unavailable")
	}
}

// Self добавляет внешний ID только к собственному профилю; roles не берутся из БД.
type Self struct {
	identity.User
	ExternalUserID string   `json:"external_user_id"`
	ProjectID      string   `json:"project_id"`
	Roles          []string `json:"roles"`
}

// selfDTO не раскрывает ban reason или сведения другого Project.
func selfDTO(a identity.Actor, u identity.User) Self {
	roles := []string{"user"}
	if a.Admin {
		roles = []string{"admin"}
	}
	return Self{u, a.ExternalID, a.ProjectID, roles}
}

// Register содержит только реально реализованные endpoints текущего этапа.
// wrap позволяет composition root добавить общие admission policies до JWT.
func Register(server *httpserver.Server, service *identity.Service, wrap func(http.Handler) http.Handler) {
	register := func(path, methods string, h http.HandlerFunc) {
		handler := Authenticate(service, Methods(methods, h))
		if wrap != nil {
			handler = wrap(handler)
		}
		server.Handle(path, handler)
	}
	register("/api/v1/me", "GET, PATCH", func(w http.ResponseWriter, r *http.Request) {
		a := Actor(r.Context())
		u := a.User
		if r.Method == "PATCH" {
			var patch identity.ProfilePatch
			if !httpserver.ReadJSON(w, r, &patch) {
				return
			}
			if patch.Validate() != nil {
				httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid profile patch")
				return
			}
			var err error
			u, err = service.Store.PatchUser(r.Context(), a, patch)
			if err != nil {
				Failure(w, r, err)
				return
			}
		}
		httpserver.WriteJSON(w, r, 200, selfDTO(a, u))
	})
	register("/api/v1/users/{user_id}", "GET", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("user_id"))
		if err != nil {
			httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid user ID")
			return
		}
		u, err := service.Store.GetUser(r.Context(), Actor(r.Context()).ProjectID, id.String())
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, u)
	})
	register("/api/v1/users", "GET", func(w http.ResponseWriter, r *http.Request) {
		after, limit, ok := Pagination(w, r)
		if !ok {
			return
		}
		users, err := service.Store.ListUsers(r.Context(), Actor(r.Context()).ProjectID, after, limit+1)
		if err != nil {
			Failure(w, r, err)
			return
		}
		var next *string
		if len(users) > limit {
			users = users[:limit]
			cursor := Cursor(users[len(users)-1].ID)
			next = &cursor
		}
		httpserver.WriteJSON(w, r, 200, struct {
			Items []identity.User `json:"items"`
			Next  *string         `json:"next_cursor"`
		}{users, next})
	})
}

// Cursor opaque для клиента; UUID encoding не даёт доступа к данным без tenant predicate.
func Cursor(id string) string { return base64.RawURLEncoding.EncodeToString([]byte(id)) }

// Pagination ограничивает нагрузку и отвергает неоднозначные повторные параметры.
func Pagination(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	q := r.URL.Query()
	limit := 50
	after := ""
	valid := true
	for k, v := range q {
		if (k != "cursor" && k != "limit") || len(v) != 1 {
			valid = false
		}
	}
	if q.Has("limit") {
		v, err := strconv.Atoi(q.Get("limit"))
		if err != nil || v < 1 || v > 100 {
			valid = false
		} else {
			limit = v
		}
	}
	if q.Has("cursor") {
		b, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		id, e := uuid.Parse(string(b))
		if err != nil || e != nil || len(b) != 36 {
			valid = false
		} else {
			after = id.String()
		}
	}
	if !valid {
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid pagination")
		return "", 0, false
	}
	return after, limit, true
}
