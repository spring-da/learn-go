package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

const QueryTimeout = 300 * time.Millisecond

type ErrorBody struct {
	Error string `json:"error"`
}

type API struct {
	repo UserRepository
}

func NewHandler(repo UserRepository) http.Handler {
	api := &API{repo: repo}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", api.getUser)                               // 这是 Go 1.22 起 http.ServeMux 新增的模式语法，格式是： [METHOD ][HOST]/[PATH]
	mux.HandleFunc("/users/{id}", func(w http.ResponseWriter, r *http.Request) { // ServeMux 不会自动生成 405 响应 那么一个 POST /users/123 请求会发生什么？ 它会落到 "/" 这个兜底 handler → 返回 404
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, ErrorBody{Error: "method not allowed"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, ErrorBody{Error: "route not found"})
	})
	return mux
}

func (api *API) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorBody{Error: "id must be a positive 64-bit integer"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), QueryTimeout)
	defer cancel()
	user, found, err := api.repo.FindByID(ctx, id)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, ErrorBody{Error: "query deadline exceeded"})
	case errors.Is(err, context.Canceled):
		// 例如客户端断开；当前请求已经取消。
		return
	case err != nil:
		log.Printf("lookup id=%d failed: %v", id, err)
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
		log.Printf("write JSON response failed: %v", err)
	}
}
