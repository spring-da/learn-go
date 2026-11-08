package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPContract(t *testing.T) {
	handler := NewHandler(NewMemoryUserRepository())
	cases := []struct {
		name, method, path string
		status             int
		errorMessage       string
	}{
		{"found", "GET", "/users/1", 200, ""},
		{"invalid text", "GET", "/users/abc", 400, "id must be a positive 64-bit integer"},
		{"zero", "GET", "/users/0", 400, "id must be a positive 64-bit integer"},
		{"negative", "GET", "/users/-1", 400, "id must be a positive 64-bit integer"},
		{"overflow", "GET", "/users/9223372036854775808", 400, "id must be a positive 64-bit integer"},
		{"missing user", "GET", "/users/404", 404, "user not found"},
		{"backend failure", "GET", "/users/500", 503, "repository unavailable"},
		{"query timeout", "GET", "/users/99", 504, "query deadline exceeded"},
		{"wrong method", "POST", "/users/1", 405, "method not allowed"},
		{"missing route", "GET", "/missing", 404, "route not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(c.method, c.path, nil))
			if recorder.Code != c.status {
				t.Fatalf("status=%d, want=%d; body=%s", recorder.Code, c.status, recorder.Body.String())
			}
			if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatal("response must be JSON")
			}
			if c.errorMessage != "" {
				var body ErrorBody
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error != c.errorMessage {
					t.Fatalf("error=%q, want=%q", body.Error, c.errorMessage)
				}
			} else {
				var user User
				if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil {
					t.Fatal(err)
				}
				if user.ID != 1 || user.Name != "Spring" {
					t.Fatalf("unexpected user: %+v", user)
				}
			}
			if c.status == http.StatusMethodNotAllowed && recorder.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("405 must advertise GET and HEAD")
			}
		})
	}
}
