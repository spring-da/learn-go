package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "api.db"))
	return NewHandler(repo)
}

func request(handler http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestCreateReadAndConflict(t *testing.T) {
	handler := newTestHandler(t)
	created := request(handler, "POST", "/users", "application/json", `{"name":"  Alice  "}`)
	if created.Code != 201 || created.Header().Get("Location") != "/users/1" {
		t.Fatalf("status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body)
	}
	var user User
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil || user.ID != 1 || user.Name != "Alice" {
		t.Fatalf("unexpected created user: %+v; error=%v", user, err)
	}
	fetched := request(handler, "GET", created.Header().Get("Location"), "", "")
	if fetched.Code != 200 || fetched.Body.String() != created.Body.String() {
		t.Fatalf("created user cannot be read: %d %s", fetched.Code, fetched.Body)
	}
	conflict := request(handler, "POST", "/users", "application/json", `{"name":"Alice"}`)
	if conflict.Code != 409 {
		t.Fatalf("duplicate status=%d, want 409", conflict.Code)
	}
}

func TestInvalidCreateRequests(t *testing.T) {
	handler := newTestHandler(t)
	cases := []struct {
		name, contentType, body string
		status                  int
	}{
		{"missing content type", "", `{"name":"Alice"}`, 415},
		{"wrong content type", "text/plain", `{"name":"Alice"}`, 415},
		{"empty body", "application/json", "", 400},
		{"broken JSON", "application/json", `{"name":`, 400},
		{"null object", "application/json", `null`, 400},
		{"array", "application/json", `[]`, 400},
		{"array with name", "application/json", `["Alice"]`, 400},
		{"wrong field type", "application/json", `{"name":123}`, 400},
		{"unknown field", "application/json", `{"name":"Alice","role":"admin"}`, 400},
		{"two objects", "application/json", `{"name":"Alice"} {"name":"Bob"}`, 400},
		{"missing name", "application/json", `{}`, 422},
		{"null name", "application/json", `{"name":null}`, 422},
		{"blank name", "application/json", `{"name":"   "}`, 422},
		{"short name", "application/json", `{"name":"A"}`, 422},
		{"long name", "application/json", `{"name":"` + strings.Repeat("a", 41) + `"}`, 422},
		{"large body", "application/json", `{"name":"` + strings.Repeat("a", 1500) + `"}`, 413},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			response := request(handler, "POST", "/users", c.contentType, c.body)
			var body ErrorBody
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Fatalf("expected JSON error: %s", response.Body)
			}
			if response.Code != c.status {
				t.Fatalf("status=%d, want=%d; body=%s", response.Code, c.status, response.Body)
			}
		})
	}
	created := request(handler, "POST", "/users", "application/json", `{"name":"学习"}`)
	var user User
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil || created.Code != 201 || user.ID != 1 {
		t.Fatalf("invalid requests must not consume IDs; got %d %s", created.Code, created.Body)
	}
}
