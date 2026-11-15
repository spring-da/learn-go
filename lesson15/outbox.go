package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type RegistrationEvent struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	Points    int64  `json:"points"`
}

type OutboxItem struct {
	Event         RegistrationEvent `json:"event"`
	Status        string            `json:"status"`
	Attempts      int64             `json:"attempts"`
	NextAttemptAt int64             `json:"next_attempt_at"`
	LastError     string            `json:"last_error"`
}

func scanOutbox(row interface{ Scan(...any) error }) (OutboxItem, error) {
	item := OutboxItem{Event: RegistrationEvent{EventType: "UserRegistered"}}
	err := row.Scan(&item.Event.EventID, &item.Event.UserID, &item.Event.Name, &item.Event.Points,
		&item.Status, &item.Attempts, &item.NextAttemptAt, &item.LastError)
	return item, err
}

const outboxColumns = "event_id, user_id, name, points, status, attempts, next_attempt_at, last_error"

func (r *SQLUserRepository) ListOutbox(ctx context.Context) ([]OutboxItem, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+outboxColumns+" FROM outbox_events ORDER BY rowid LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]OutboxItem, 0)
	for rows.Next() {
		item, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *SQLUserRepository) nextEvent(ctx context.Context, now int64) (OutboxItem, bool, error) {
	item, err := scanOutbox(r.db.QueryRowContext(ctx,
		"SELECT "+outboxColumns+" FROM outbox_events WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY rowid LIMIT 1", now))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxItem{}, false, nil
	}
	return item, err == nil, err
}

type HTTPEventSender struct {
	Client *http.Client
	URL    string
}

func (s *HTTPEventSender) Send(ctx context.Context, event RegistrationEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

// now 是 Unix 毫秒；显式传入方便验证重试时间，无需测试等待真实定时器。
func DispatchOnce(ctx context.Context, repo *SQLUserRepository, sender *HTTPEventSender, now int64, retryDelay time.Duration) (bool, error) {
	item, found, err := repo.nextEvent(ctx, now)
	if err != nil || !found {
		return false, err
	}
	// QueryRow 已读取完毕；HTTP 调用期间没有持有数据库事务。
	if err := sender.Send(ctx, item.Event); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		_, updateErr := repo.db.ExecContext(ctx, `UPDATE outbox_events SET attempts = attempts + 1,
			next_attempt_at = ?, last_error = ? WHERE event_id = ? AND status = 'pending'`,
			now+retryDelay.Milliseconds(), err.Error(), item.Event.EventID)
		if updateErr == nil {
			log.Printf("event %s: delivery failed, retry scheduled: %v", item.Event.EventID, err)
		}
		return true, updateErr
	}
	_, err = repo.db.ExecContext(ctx, `UPDATE outbox_events SET status = 'sent',
		attempts = attempts + 1, next_attempt_at = 0, last_error = '' WHERE event_id = ? AND status = 'pending'`, item.Event.EventID)
	if err == nil {
		log.Printf("event %s: delivery confirmed", item.Event.EventID)
	}
	return true, err
}

func RunWorker(ctx context.Context, repo *SQLUserRepository, sender *HTTPEventSender, interval, retryDelay time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := DispatchOnce(ctx, repo, sender, time.Now().UnixMilli(), retryDelay); err != nil && ctx.Err() == nil {
			log.Printf("outbox worker: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
