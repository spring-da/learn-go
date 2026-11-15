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
	"time"
)

type CreateTransferRequest struct {
	FromUserID *int64 `json:"from_user_id"`
	ToUserID   *int64 `json:"to_user_id"`
	Amount     *int64 `json:"amount"`
}

func (api *API) createTransfer(w http.ResponseWriter, r *http.Request) {
	key, valid := parseIdempotencyKey(r)
	if !valid {
		writeJSON(w, 400, ErrorBody{Error: InvalidKeyMessage})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !(mediaType == "application/json" || (strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		writeJSON(w, 415, ErrorBody{Error: "Content-Type must be application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxCreateBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input *CreateTransferRequest
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input == nil || input.FromUserID == nil || input.ToUserID == nil || input.Amount == nil {
		writeJSON(w, 400, ErrorBody{Error: "invalid JSON body"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeDecodeError(w, err)
		return
	}
	result, err := api.repo.Transfer(r.Context(), key, *input.FromUserID, *input.ToUserID, *input.Amount)
	switch {
	case errors.Is(err, ErrInvalidTransfer):
		writeJSON(w, 422, ErrorBody{Error: err.Error()})
	case errors.Is(err, ErrTransferKeyMismatch), errors.Is(err, ErrInsufficientPoints), errors.Is(err, ErrPointsOverflow), errors.Is(err, ErrVersionExhausted):
		writeJSON(w, 409, ErrorBody{Error: err.Error()})
	case errors.Is(err, ErrUserNotFound):
		writeJSON(w, 404, ErrorBody{Error: "user not found"})
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		log.Printf("transfer failed: %v", err)
		writeJSON(w, 503, ErrorBody{Error: "repository unavailable"})
	default:
		w.Header().Set("Location", fmt.Sprintf("/transfers/%d", result.Transfer.ID))
		w.Header().Set("Idempotency-Replayed", strconv.FormatBool(result.Replayed))
		writeJSON(w, 201, result.Transfer)
	}
}

func (api *API) getTransfer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, ErrorBody{Error: "id must be a positive 64-bit integer"})
		return
	}
	if receipt, found := api.receipts.GetAt(id, time.Now()); found {
		w.Header().Set("X-Receipt-Cache", "HIT")
		writeJSON(w, 200, receipt)
		return
	}
	w.Header().Set("X-Receipt-Cache", "MISS")
	api.receiptLoads.Add(1)
	receipt, found, err := api.repo.FindTransfer(r.Context(), id)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		writeJSON(w, 503, ErrorBody{Error: "repository unavailable"})
	case !found:
		writeJSON(w, 404, ErrorBody{Error: "transfer not found"})
	default:
		api.receipts.PutAt(id, receipt, time.Now())
		writeJSON(w, 200, receipt)
	}
}

func (api *API) cacheStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, struct {
		CacheStats
		DatabaseLoads uint64 `json:"database_loads"`
	}{CacheStats: api.receipts.StatsAt(time.Now()), DatabaseLoads: api.receiptLoads.Load()})
}

func (api *API) deleteReceiptCache(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, ErrorBody{Error: "id must be a positive 64-bit integer"})
		return
	}
	api.receipts.Delete(id)
	w.WriteHeader(204)
}
