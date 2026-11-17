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

type ErrorBody struct {
	Error string `json:"error"`
}

type CreateUserRequest struct {
	Name *string `json:"name"`
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
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !(mediaType == "application/json" || (strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		writeJSON(w, http.StatusUnsupportedMediaType, ErrorBody{Error: "Content-Type must be application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxCreateBody) // MaxBytesReader 包一层，超过 MaxCreateBody 字节数就报错，防止恶意超大请求（DoS 防护） 注意它是替换 r.Body，之后所有读取都受限制。
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()                // SON 里出现结构体没有的字段就报错。这样能及时发现客户端字段拼写错误（如把 name 写成 nmae），而不是静默忽略。属于严格 API 设计。
	var input *CreateUserRequest                   // 此时 input 本身不是一个 CreateUserRequest 对象，它只是"一个能存放 CreateUserRequest 地址的变量"，现在里面是 nil
	if err := decoder.Decode(&input); err != nil { // & 是取地址运算符，&input 得到的是"变量 input 自己的内存地址"
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
	user, err := api.repo.Create(r.Context(), name)
	switch {
	case errors.Is(err, ErrNameExists):
		writeJSON(w, http.StatusConflict, ErrorBody{Error: "name already exists"})
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		log.Printf("create user failed: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, ErrorBody{Error: "repository unavailable"})
	default:
		w.Header().Set("Location", fmt.Sprintf("/users/%d", user.ID))
		writeJSON(w, http.StatusCreated, user)
	}
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
