package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxCreateBody = 1024

const InvalidKeyMessage = "Idempotency-Key must be 1-64 ASCII letters, digits, '-' or '_'"

type ErrorBody struct {
	Error string `json:"error"`
}

type CreateUserRequest struct {
	Name          *string `json:"name"`
	InitialPoints *int64  `json:"initial_points"`
}

type API struct {
	repo UserRepository
}

func NewHandler(repo UserRepository) http.Handler {
	api := &API{repo: repo}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", api.createUser)
	mux.HandleFunc("GET /users/{id}", api.getUser)
	mux.HandleFunc("/users", methodNotAllowed("POST"))
	mux.HandleFunc("/users/{id}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, ErrorBody{Error: "route not found"})
	})
	return mux
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, ErrorBody{Error: "method not allowed"})
	}
}

func (api *API) createUser(w http.ResponseWriter, r *http.Request) {
	key, valid := parseIdempotencyKey(r)
	if !valid {
		writeJSON(w, http.StatusBadRequest, ErrorBody{Error: InvalidKeyMessage})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !(mediaType == "application/json" || (strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		writeJSON(w, http.StatusUnsupportedMediaType, ErrorBody{Error: "Content-Type must be application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxCreateBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input *CreateUserRequest
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input == nil {
		writeJSON(w, http.StatusBadRequest, ErrorBody{Error: "invalid JSON body"})
		return
	}
	// 必须读到 EOF，避免只接受第一个对象而忽略其后的内容。
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeDecodeError(w, err)
		return
	}
	name := ""
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if length := utf8.RuneCountInString(name); length < 2 || length > 40 {
		writeJSON(w, http.StatusUnprocessableEntity, ErrorBody{Error: "name must contain 2 to 40 characters"})
		return
	}
	initialPoints := int64(0)
	if input.InitialPoints != nil {
		initialPoints = *input.InitialPoints
	}
	result, err := api.repo.Create(r.Context(), key, name, initialPoints)
	switch {
	case errors.Is(err, ErrKeyMismatch):
		writeJSON(w, http.StatusConflict, ErrorBody{Error: "idempotency key was used with different parameters"})
	case errors.Is(err, ErrInvalidPoints):
		writeJSON(w, http.StatusUnprocessableEntity, ErrorBody{Error: "initial points must be non-negative"})
	case errors.Is(err, ErrNameExists):
		writeJSON(w, http.StatusConflict, ErrorBody{Error: "name already exists"})
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		log.Printf("create user failed: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, ErrorBody{Error: "repository unavailable"})
	default:
		w.Header().Set("Location", fmt.Sprintf("/users/%d", result.User.ID))
		w.Header().Set("Idempotency-Replayed", strconv.FormatBool(result.Replayed))
		writeJSON(w, http.StatusCreated, result.User)
	}
}

func parseIdempotencyKey(r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", false
	}
	key := values[0]
	if len(key) < 1 || len(key) > 64 {
		return "", false
	}
	for index := 0; index < len(key); index++ {
		b := key[index]
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_') {
			return "", false
		}
	}
	return key, true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorBody{Error: "request body too large"})
		return
	}
	writeJSON(w, http.StatusBadRequest, ErrorBody{Error: "invalid JSON body"})
}

func (api *API) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorBody{Error: "id must be a positive 64-bit integer"})
		return
	}
	user, found, err := api.repo.FindByID(r.Context(), id)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		writeJSON(w, http.StatusServiceUnavailable, ErrorBody{Error: "repository unavailable"})
	case !found:
		writeJSON(w, http.StatusNotFound, ErrorBody{Error: "user not found"})
	default:
		writeJSON(w, http.StatusOK, user)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response failed: %v", err)
	}
}
