package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"unicode/utf8"
)

const notificationSchema = `CREATE TABLE IF NOT EXISTS notifications (
	event_id TEXT PRIMARY KEY NOT NULL,
	user_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	points INTEGER NOT NULL
);`

var ErrEventMismatch = errors.New("event id was used with different parameters")

type NotificationRepository struct{ db *sql.DB }

func OpenNotifications(ctx context.Context, path string) (*NotificationRepository, error) {
	db, err := openDatabase(ctx, path, notificationSchema)
	if err != nil {
		return nil, err
	}
	return &NotificationRepository{db: db}, nil
}

func (r *NotificationRepository) Close() error { return r.db.Close() }

func (r *NotificationRepository) Accept(ctx context.Context, event RegistrationEvent) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO notifications (event_id, user_id, name, points)
		VALUES (?, ?, ?, ?) ON CONFLICT(event_id) DO NOTHING`, event.EventID, event.UserID, event.Name, event.Points)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		var userID, points int64
		var name string
		if err := tx.QueryRowContext(ctx, "SELECT user_id, name, points FROM notifications WHERE event_id = ?", event.EventID).Scan(&userID, &name, &points); err != nil {
			return false, err
		}
		if userID != event.UserID || name != event.Name || points != event.Points {
			return false, ErrEventMismatch
		}
	}
	return inserted == 0, tx.Commit()
}

func (r *NotificationRepository) List(ctx context.Context) ([]RegistrationEvent, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT event_id, user_id, name, points FROM notifications ORDER BY rowid LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]RegistrationEvent, 0)
	for rows.Next() {
		event := RegistrationEvent{EventType: "UserRegistered"}
		if err := rows.Scan(&event.EventID, &event.UserID, &event.Name, &event.Points); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func validEvent(event RegistrationEvent) bool {
	if len(event.EventID) != 32 || event.EventType != "UserRegistered" || event.UserID <= 0 || event.Points < 0 {
		return false
	}
	for _, b := range []byte(event.EventID) {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	length := utf8.RuneCountInString(event.Name)
	return length >= 2 && length <= 40
}

func NewNotificationHandler(repo *NotificationRepository, failFirst, loseAckOnce bool) http.Handler {
	var fail, loseAck atomic.Bool
	fail.Store(failFirst)
	loseAck.Store(loseAckOnce)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /notifications", func(w http.ResponseWriter, r *http.Request) {
		events, err := repo.List(r.Context())
		if err != nil {
			writeJSON(w, 503, ErrorBody{Error: "notification repository unavailable"})
			return
		}
		writeJSON(w, 200, events)
	})
	mux.HandleFunc("POST /notifications", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var event RegistrationEvent
		if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || !validEvent(event) {
			writeJSON(w, 400, ErrorBody{Error: "invalid notification event"})
			return
		}
		if fail.Swap(false) {
			writeJSON(w, 503, ErrorBody{Error: "simulated failure before processing"})
			return
		}
		replayed, err := repo.Accept(r.Context(), event)
		if errors.Is(err, ErrEventMismatch) {
			writeJSON(w, 409, ErrorBody{Error: err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, 503, ErrorBody{Error: "notification repository unavailable"})
			return
		}
		// 接收端已提交，但不给成功确认，模拟发送端无法确定是否送达。
		if loseAck.Swap(false) {
			writeJSON(w, 503, ErrorBody{Error: "simulated failure after processing"})
			return
		}
		writeJSON(w, 200, map[string]bool{"replayed": replayed})
	})
	mux.HandleFunc("/notifications", methodNotAllowed("GET, HEAD, POST"))
	return mux
}
