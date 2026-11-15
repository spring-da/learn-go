package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

type ReplacePointsRequest struct {
	Points          *int64 `json:"points"`
	ExpectedVersion *int64 `json:"expected_version"`
}

func (api *API) replacePoints(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, ErrorBody{Error: "id must be a positive 64-bit integer"})
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
	var input *ReplacePointsRequest
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input == nil || input.Points == nil || input.ExpectedVersion == nil {
		writeJSON(w, 400, ErrorBody{Error: "invalid JSON body"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeDecodeError(w, err)
		return
	}
	account, err := api.repo.ReplacePoints(r.Context(), id, *input.Points, *input.ExpectedVersion)
	switch {
	case errors.Is(err, ErrInvalidVersion), errors.Is(err, ErrInvalidUpdatedPoints):
		writeJSON(w, 422, ErrorBody{Error: err.Error()})
	case errors.Is(err, ErrVersionConflict), errors.Is(err, ErrVersionExhausted):
		writeJSON(w, 409, ErrorBody{Error: err.Error()})
	case errors.Is(err, ErrUserNotFound):
		writeJSON(w, 404, ErrorBody{Error: "user not found"})
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		log.Printf("replace points failed: %v", err)
		writeJSON(w, 503, ErrorBody{Error: "repository unavailable"})
	default:
		writeJSON(w, 200, account)
	}
}
