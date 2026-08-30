package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrUnavailable is distinct from bad credentials: clients receive 503 and should
// not discard a valid login merely because Auth is temporarily unreachable.
var ErrUnavailable = errors.New("AUTH_UNAVAILABLE")

// Remote validates identity against one operator-configured Auth endpoint. It never
// copies Auth roles or caches positive responses. Auth checks the signature, expiration,
// account and session on every request, so logout also revokes access to Chat.
// One deployment serves its configured Project; clients cannot select another tenant.
type Remote struct {
	endpoint, projectID string
	client              *http.Client
	projects            Resolver
}

// NewRemote validates trusted configuration before bind. Production requires TLS;
// redirects and environment HTTP proxies are disabled to avoid leaking bearer tokens.
func NewRemote(baseURL, projectID, environment string, projects Resolver) (*Remote, error) {
	u, err := url.Parse(baseURL)
	id, e := uuid.Parse(projectID)
	if err != nil || e != nil || id == uuid.Nil || projectID != id.String() || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") || (u.Scheme != "https" && environment != "development" && environment != "test") {
		return nil, errors.New("AUTH_REMOTE_CONFIG_INVALID")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 2 * time.Second
	transport.MaxResponseHeaderBytes = 16384
	return &Remote{endpoint: strings.TrimRight(baseURL, "/") + "/api/v1/users/me", projectID: projectID, projects: projects,
		client: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// WithProjects attaches the runtime store after infrastructure has opened.
func (v *Remote) WithProjects(projects Resolver) *Remote {
	copy := *v
	copy.projects = projects
	return &copy
}

// Verify treats Auth's successful response as the identity proof. Only the numeric
// user ID is consumed; roles/profile fields are deliberately ignored. Local Project
// activation is checked before auto-provisioning and again inside its transaction.
func (v *Remote) Verify(ctx context.Context, raw string) (Identity, error) {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n") {
		return Identity{}, ErrUnauthenticated
	}
	if v.projects == nil {
		return Identity{}, ErrUnavailable
	}
	p, err := v.projects.AuthProject(ctx, v.projectID)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	if p.ID != v.projectID || !p.Active {
		return Identity{}, ErrUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint, nil)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Identity{}, ErrUnauthenticated
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(body) > 16384 {
		return Identity{}, ErrUnavailable
	}
	var me struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(body, &me) != nil || me.ID <= 0 {
		return Identity{}, ErrUnavailable
	}
	return Identity{ProjectID: v.projectID, ExternalID: strconv.FormatInt(me.ID, 10)}, nil
}
