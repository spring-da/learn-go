package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func run() error {
	dbPath := envOr("DB_PATH", "data/outbox.db")
	notificationPath := envOr("NOTIFY_DB_PATH", "data/notifications.db")
	for _, path := range []string{dbPath, notificationPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	repo, err := OpenUserRepository(context.Background(), dbPath)
	if err != nil {
		return err
	}
	defer repo.Close()
	notifications, err := OpenNotifications(context.Background(), notificationPath)
	if err != nil {
		return err
	}
	defer notifications.Close()
	listener, err := net.Listen("tcp", envOr("HTTP_ADDR", "127.0.0.1:8080"))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/notifications", NewNotificationHandler(notifications, envOr("DEMO_FAIL_FIRST", "1") == "1", os.Getenv("DEMO_LOSE_ACK_ONCE") == "1"))
	mux.Handle("/", NewHandler(repo))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	notifyURL := envOr("NOTIFY_URL", fmt.Sprintf("http://127.0.0.1:%d/notifications", listener.Addr().(*net.TCPAddr).Port))
	go func() {
		defer close(workerDone)
		if envOr("WORKER_ENABLED", "1") == "1" {
			RunWorker(workerCtx, repo, &HTTPEventSender{Client: &http.Client{Timeout: 2 * time.Second}, URL: notifyURL}, time.Second, 2*time.Second)
		}
	}()
	log.Printf("database: %s; notification database: %s", dbPath, notificationPath)
	log.Printf("Go outbox API listening at http://%s", listener.Addr())
	log.Printf("worker enabled: %t; notification URL: %s", envOr("WORKER_ENABLED", "1") == "1", notifyURL)
	select {
	case <-ctx.Done():
	case err = <-serveErr:
	}
	cancelWorker()
	<-workerDone
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		return shutdownErr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
