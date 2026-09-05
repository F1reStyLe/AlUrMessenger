// Package admission applies exact CORS and shared Redis limits to business routes.
// Probes/docs remain independent of these route policies.
package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/redis/go-redis/v9"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config cannot disable limits or opt into wildcard credentialed CORS.
type Config struct {
	Origins                    map[string]bool
	IPPerMinute, UserPerMinute int
}

// Load rejects malformed/non-origin URLs and insecure production origins before bind.
func Load(environment string) (Config, error) {
	c := Config{Origins: map[string]bool{}, IPPerMinute: 120, UserPerMinute: 60}
	for name, target := range map[string]*int{"RATE_IP_PER_MINUTE": &c.IPPerMinute, "RATE_USER_PER_MINUTE": &c.UserPerMinute} {
		if raw, ok := os.LookupEnv(name); ok {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > 100000 {
				return Config{}, errors.New("RATE_CONFIG_INVALID")
			}
			*target = v
		}
	}
	origins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if origins == "" {
		return c, nil
	}
	for _, origin := range strings.Split(origins, ",") {
		origin = strings.TrimSpace(origin)
		u, err := url.Parse(origin)
		if err != nil || u.Hostname() == "" || u.User != nil || origin != u.Scheme+"://"+u.Host || u.Opaque != "" || (u.Scheme != "https" && (environment == "production" || u.Scheme != "http")) || strings.Contains(u.Host, "*") {
			return Config{}, errors.New("CORS_CONFIG_INVALID")
		}
		c.Origins[origin] = true
	}
	return c, nil
}

// Limiter is a shared fixed window starting with the first request, using Redis time/TTL.
// There is no local fallback: an outage fails closed, with no per-instance bypass.
type Limiter struct {
	Redis  *redis.Client
	Config Config
}

// counter increments and sets expiry atomically, so crashes cannot leave immortal keys.
var counter = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],60000) end; return {n,redis.call('PTTL',KEYS[1])}`)

// Take exposes the primitive for other transports; returned retry is rounded up seconds.
func (l *Limiter) Take(ctx context.Context, key string, limit int) (bool, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	result, err := counter.Run(ctx, l.Redis, []string{"alur:limit:" + key}).Int64Slice()
	if err != nil || len(result) != 2 {
		if err == nil {
			err = errors.New("RATE_RESULT_INVALID")
		}
		return false, 0, err
	}
	ttl := result[1]
	if ttl < 1 {
		ttl = 1
	}
	return result[0] <= int64(limit), int((ttl + 999) / 1000), nil
}

// PeerIP ignores Forwarded/X-Forwarded-For until an explicit trusted-proxy deployment exists.
func PeerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return addr.Unmap().String()
}

// admit maps Redis failures safely; exceeded limits never continue to the use case.
func (l *Limiter) admit(w http.ResponseWriter, r *http.Request, key string, limit int) bool {
	allowed, retry, err := l.Take(r.Context(), key, limit)
	if err != nil {
		httpserver.WriteError(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Rate limiter unavailable")
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		httpserver.WriteError(w, r, 429, "RATE_LIMITED", "Request limit exceeded")
		return false
	}
	return true
}

// Before runs per-IP limiting before authentication and handles CORS preflight without JWT.
func (l *Limiter) Before(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := PeerIP(r)
		if ip == "" {
			httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid peer address")
			return
		}
		sum := sha256.Sum256([]byte(ip))
		if !l.admit(w, r, "ip:"+hex.EncodeToString(sum[:]), l.Config.IPPerMinute) {
			return
		}
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if len(r.Header.Values("Origin")) > 1 || (origin != "" && !l.Config.Origins[origin]) {
			httpserver.WriteError(w, r, 403, "ORIGIN_FORBIDDEN", "Origin not allowed")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
		}
		if r.Method == http.MethodOptions && origin != "" {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			method := r.Header.Get("Access-Control-Request-Method")
			// Exact route shapes allow only implemented mutations, including relation
			// items. Collection routes must not accidentally inherit PUT/DELETE.
			conversationItem := strings.HasPrefix(r.URL.Path, "/api/v1/conversations/") && strings.Count(r.URL.Path, "/") == 4
			segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			memberCollection := len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "conversations" && segments[4] == "members"
			memberItem := len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "conversations" && segments[4] == "members"
			allowed := method == "GET" || (method == "POST" && r.URL.Path == "/api/v1/conversations") || (method == "PATCH" && (conversationItem || r.URL.Path == "/api/v1/me" || r.URL.Path == "/admin/v1/project" || r.URL.Path == "/admin/v1/feature-flags"))
			allowed = allowed || (method == "POST" && memberCollection) || ((method == "PATCH" || method == "DELETE") && memberItem)
			messageCollection := len(segments) == 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "conversations" && (segments[4] == "messages" || segments[4] == "read" || segments[4] == "delivered")
			allowed = allowed || (method == "POST" && messageCollection)
			messageItem := len(segments) == 4 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "messages"
			allowed = allowed || (messageItem && (method == "PATCH" || method == "DELETE"))
			reactionItem := len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "messages" && segments[4] == "reactions"
			pinItem := len(segments) == 6 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "conversations" && segments[4] == "pins"
			allowed = allowed || ((reactionItem || pinItem) && (method == "PUT" || method == "DELETE"))
			blacklistCollection := r.URL.Path == "/admin/v1/blacklist"
			blacklistItem := len(segments) == 4 && segments[0] == "admin" && segments[1] == "v1" && segments[2] == "blacklist"
			allowed = allowed || (blacklistCollection && method == "POST") || (blacklistItem && (method == "PATCH" || method == "DELETE"))
			for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				switch strings.ToLower(strings.TrimSpace(h)) {
				case "", "authorization", "content-type", "x-request-id":
				default:
					allowed = false
				}
			}
			if !allowed {
				httpserver.WriteError(w, r, 403, "ORIGIN_FORBIDDEN", "Preflight not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", method)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "300")
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// After runs only with a verified Actor; key includes Project as well as internal User.
func (l *Limiter) After(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		if a.User.ID == "" {
			httpserver.WriteError(w, r, 401, "UNAUTHENTICATED", "Actor required")
			return
		}
		if l.admit(w, r, "user:"+a.ProjectID+":"+a.User.ID, l.Config.UserPerMinute) {
			next.ServeHTTP(w, r)
		}
	})
}
