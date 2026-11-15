package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestNotifications(t *testing.T, path string) *NotificationRepository {
	t.Helper()
	repo, err := OpenNotifications(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func oneOutbox(t *testing.T, repo *SQLUserRepository) OutboxItem {
	t.Helper()
	items, err := repo.ListOutbox(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("outbox=%+v err=%v", items, err)
	}
	return items[0]
}

func registerForDelivery(t *testing.T, repo *SQLUserRepository) {
	t.Helper()
	if _, err := repo.Create(context.Background(), "outbox-key", "Alice", 100); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxInsertFailureRollsBackAllWrites(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "atomic.db"))
	_, err := repo.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON outbox_events
		BEGIN SELECT RAISE(ABORT, 'simulated outbox write failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(context.Background(), "outbox-key", "Alice", 100); err == nil {
		t.Fatal("expected outbox failure")
	}
	assertCounts(t, repo, 0, 0, 0)
	if _, err := repo.db.Exec("DROP TRIGGER reject_event"); err != nil {
		t.Fatal(err)
	}
	registerForDelivery(t, repo)
	first := oneOutbox(t, repo)
	if !validEvent(first.Event) || first.Status != "pending" || first.Attempts != 0 {
		t.Fatalf("invalid event: %+v", first)
	}
	registerForDelivery(t, repo)
	if next := oneOutbox(t, repo); next.Event.EventID != first.Event.EventID {
		t.Fatal("request replay created a new event")
	}
	assertCounts(t, repo, 1, 1, 1)
}

func TestDeliveryRetriesOnlyWhenDue(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := openTestRepository(t, filepath.Join(dir, "outbox.db"))
	notifications := openTestNotifications(t, filepath.Join(dir, "notifications.db"))
	server := httptest.NewServer(NewNotificationHandler(notifications, true, false))
	defer server.Close()
	sender := &HTTPEventSender{Client: server.Client(), URL: server.URL + "/notifications"}
	registerForDelivery(t, repo)
	if worked, err := DispatchOnce(ctx, repo, sender, 1000, 2*time.Second); err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
	first := oneOutbox(t, repo)
	if first.Status != "pending" || first.Attempts != 1 || first.NextAttemptAt != 3000 || first.LastError == "" {
		t.Fatalf("missing retry state: %+v", first)
	}
	if events, _ := notifications.List(ctx); len(events) != 0 {
		t.Fatal("first failed request produced a notification")
	}
	if worked, err := DispatchOnce(ctx, repo, sender, 2999, 2*time.Second); err != nil || worked {
		t.Fatalf("retried too early: %t %v", worked, err)
	}
	if worked, err := DispatchOnce(ctx, repo, sender, 3000, 2*time.Second); err != nil || !worked {
		t.Fatalf("retry failed: %t %v", worked, err)
	}
	last := oneOutbox(t, repo)
	if last.Status != "sent" || last.Attempts != 2 || last.LastError != "" {
		t.Fatalf("invalid final state: %+v", last)
	}
	if events, _ := notifications.List(ctx); len(events) != 1 {
		t.Fatal("expected one notification")
	}
	if worked, err := DispatchOnce(ctx, repo, sender, 4000, 2*time.Second); err != nil || worked {
		t.Fatalf("sent event was delivered again: %t %v", worked, err)
	}
}

func TestLostAcknowledgementSurvivesBothRepositoriesReopening(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, receiverPath := filepath.Join(dir, "outbox.db"), filepath.Join(dir, "notifications.db")
	repo := openTestRepository(t, path)
	notifications := openTestNotifications(t, receiverPath)
	server := httptest.NewServer(NewNotificationHandler(notifications, false, true))
	registerForDelivery(t, repo)
	if _, err := DispatchOnce(ctx, repo, &HTTPEventSender{Client: server.Client(), URL: server.URL + "/notifications"}, 1000, time.Second); err != nil {
		t.Fatal(err)
	}
	eventID := oneOutbox(t, repo).Event.EventID
	if events, _ := notifications.List(ctx); len(events) != 1 {
		t.Fatal("receiver did not commit before failed acknowledgement")
	}
	server.Close()
	repo.Close()
	notifications.Close()
	reopened := openTestRepository(t, path)
	receiver := openTestNotifications(t, receiverPath)
	restarted := httptest.NewServer(NewNotificationHandler(receiver, false, false))
	defer restarted.Close()
	if _, err := DispatchOnce(ctx, reopened, &HTTPEventSender{Client: restarted.Client(), URL: restarted.URL + "/notifications"}, 2000, time.Second); err != nil {
		t.Fatal(err)
	}
	last := oneOutbox(t, reopened)
	if last.Event.EventID != eventID || last.Status != "sent" || last.Attempts != 2 {
		t.Fatalf("invalid reopened state: %+v", last)
	}
	if events, _ := receiver.List(ctx); len(events) != 1 {
		t.Fatal("duplicate effect after receiver restarted")
	}
}

func TestMarkSentFailureAllowsSafeRedelivery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := openTestRepository(t, filepath.Join(dir, "outbox.db"))
	notifications := openTestNotifications(t, filepath.Join(dir, "notifications.db"))
	server := httptest.NewServer(NewNotificationHandler(notifications, false, false))
	defer server.Close()
	sender := &HTTPEventSender{Client: server.Client(), URL: server.URL + "/notifications"}
	registerForDelivery(t, repo)
	if _, err := repo.db.Exec(`CREATE TRIGGER reject_mark BEFORE UPDATE ON outbox_events WHEN NEW.status = 'sent'
		BEGIN SELECT RAISE(ABORT, 'simulated crash before marking sent'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := DispatchOnce(ctx, repo, sender, 1000, time.Second); err == nil {
		t.Fatal("expected mark failure")
	}
	if oneOutbox(t, repo).Status != "pending" {
		t.Fatal("failed marker should leave pending event")
	}
	if events, _ := notifications.List(ctx); len(events) != 1 {
		t.Fatal("receiver did not process event")
	}
	if _, err := repo.db.Exec("DROP TRIGGER reject_mark"); err != nil {
		t.Fatal(err)
	}
	if _, err := DispatchOnce(ctx, repo, sender, 2000, time.Second); err != nil {
		t.Fatal(err)
	}
	if oneOutbox(t, repo).Status != "sent" {
		t.Fatal("event was not confirmed")
	}
	if events, _ := notifications.List(ctx); len(events) != 1 {
		t.Fatal("redelivery produced duplicate effect")
	}
}

func TestReceiverConcurrentDuplicatesAndParameterMismatch(t *testing.T) {
	ctx := context.Background()
	repo := openTestNotifications(t, filepath.Join(t.TempDir(), "notifications.db"))
	server := httptest.NewServer(NewNotificationHandler(repo, false, false))
	defer server.Close()
	event := RegistrationEvent{EventID: "0123456789abcdef0123456789abcdef", EventType: "UserRegistered", UserID: 1, Name: "Alice", Points: 100}
	body, _ := json.Marshal(event)
	var wait sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := server.Client().Post(server.URL+"/notifications", "application/json", bytes.NewReader(body))
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != 200 {
					t.Errorf("duplicate HTTP status=%d", response.StatusCode)
				}
			}
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if events, _ := repo.List(ctx); len(events) != 1 {
		t.Fatal("concurrent duplicates produced multiple effects")
	}
	event.Points = 101
	body, _ = json.Marshal(event)
	response, err := server.Client().Post(server.URL+"/notifications", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatalf("mismatched event status=%d", response.StatusCode)
	}
}

func TestWorkerCancellationDuringHTTP(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "outbox.db"))
	registerForDelivery(t, repo)
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunWorker(ctx, repo, &HTTPEventSender{Client: server.Client(), URL: server.URL}, time.Hour, time.Second)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not send")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if oneOutbox(t, repo).Status != "pending" {
		t.Fatal("cancelled delivery must remain retryable")
	}
}
