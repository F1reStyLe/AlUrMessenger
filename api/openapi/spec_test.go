package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestContract catches broken internal refs, duplicate operation IDs and accidental
// anonymous business endpoints. Full OpenAPI schema validation is a separate tool.
func TestContract(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(Document, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatal("remote reference forbidden", ref)
				}
				var target any = doc
				for _, part := range strings.Split(ref[2:], "/") {
					object, ok := target.(map[string]any)
					if !ok {
						t.Fatal("invalid reference", ref)
					}
					target, ok = object[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
					if !ok {
						t.Fatal("unresolved reference", ref)
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	paths := doc["paths"].(map[string]any)
	seen := map[string]bool{}
	for path, item := range paths {
		for method, raw := range item.(map[string]any) {
			if method == "parameters" || strings.HasPrefix(method, "x-") {
				continue
			}
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" || seen[id] {
				t.Fatal("missing/duplicate operation ID", path, method)
			}
			seen[id] = true
			// Every method on a templated path needs its path parameter, including preflight.
			parameters := map[string]bool{}
			for _, container := range []map[string]any{item.(map[string]any), op} {
				if list, ok := container["parameters"].([]any); ok {
					for _, entry := range list {
						p := entry.(map[string]any)
						if p["in"] == "path" && p["required"] == true {
							name, _ := p["name"].(string)
							parameters[name] = true
						}
					}
				}
			}
			for _, part := range strings.Split(path, "/") {
				if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && !parameters[part[1:len(part)-1]] {
					t.Fatal("path parameter missing", path, method)
				}
			}
			if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/admin/") {
				if method != "options" {
					security, ok := op["security"].([]any)
					if !ok || len(security) != 1 {
						t.Fatal("business auth missing", path)
					}
				}
			}
		}
	}
	for path, methods := range map[string][]string{"/health/live": {"get", "head"}, "/health/ready": {"get", "head"}, "/api/v1/me": {"get", "patch", "options"}, "/api/v1/users": {"get", "options"}, "/api/v1/users/{user_id}": {"get", "options"}, "/admin/v1/project": {"get", "patch", "options"}, "/admin/v1/feature-flags": {"get", "patch", "options"}, "/admin/v1/audit-logs": {"get", "options"}, "/docs/api": {"get", "head"}, "/docs/api/openapi.json": {"get", "head"}} {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatal("implemented route undocumented", path)
		}
		for _, method := range methods {
			if _, ok = item[method]; !ok {
				t.Fatal("implemented method undocumented", path, method)
			}
		}
	}
}

// TestConversationMessageAndRecoveryRoutes keeps every implemented operation
// visible in Swagger, including receipt bodies and the separate WS upgrade contract.
func TestConversationMessageAndRecoveryRoutes(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(Document, &doc); err != nil {
		t.Fatal(err)
	}
	for path, methods := range map[string][]string{
		"/api/v1/conversations":                        {"get", "post", "options"},
		"/api/v1/attachments":                          {"post", "options"},
		"/api/v1/attachments/{id}":                     {"get", "options"},
		"/api/v1/attachments/{id}/download-url":        {"post", "options"},
		"/api/v1/conversations/{id}":                   {"get", "patch", "options"},
		"/api/v1/conversations/{id}/members":           {"get", "post", "options"},
		"/api/v1/conversations/{id}/members/{user_id}": {"patch", "delete", "options"},
		"/api/v1/conversations/{id}/messages":          {"get", "post", "options"},
		"/api/v1/messages/{id}":                        {"get", "patch", "delete", "options"},
		"/api/v1/messages/{id}/reactions/{reaction}":   {"put", "delete", "options"},
		"/api/v1/conversations/{id}/pins":              {"get", "options"},
		"/api/v1/conversations/{id}/pins/{message_id}": {"put", "delete", "options"},
		"/api/v1/conversations/{id}/search":            {"get", "options"},
		"/api/v1/conversations/{id}/events":            {"get", "options"},
		"/api/v1/conversations/{id}/snapshot":          {"get", "options"},
		"/api/v1/conversations/{id}/read":              {"post", "options"},
		"/api/v1/conversations/{id}/delivered":         {"post", "options"},
		"/ws":                                          {"get"},
	} {
		for _, method := range methods {
			if len(doc.Paths[path][method]) == 0 {
				t.Fatal("implemented route undocumented", method, path)
			}
		}
	}
}
