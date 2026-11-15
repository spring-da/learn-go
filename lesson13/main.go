package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	graceMS, err := strconv.ParseInt(envOr("SHUTDOWN_GRACE_MS", "5000"), 10, 64)
	if err != nil || graceMS < 1 || graceMS > 10000 {
		return fmt.Errorf("SHUTDOWN_GRACE_MS must be 1 to 10000")
	}
	dbPath := envOr("DB_PATH", "data/shutdown.db")
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
	lifecycle := NewLifecycle()
	business := http.NewServeMux()
	business.Handle("/notifications", NewNotificationHandler(notifications, envOr("DEMO_FAIL_FIRST", "1") == "1", os.Getenv("DEMO_LOSE_ACK_ONCE") == "1"))
	business.Handle("/", NewHandler(repo))
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerDone := make(chan struct{})
	go func() { <-lifecycle.stopping; cancelWorker() }()
	notifyURL := envOr("NOTIFY_URL", fmt.Sprintf("http://127.0.0.1:%d/notifications", listener.Addr().(*net.TCPAddr).Port))
	go func() {
		defer close(workerDone)
		if envOr("WORKER_ENABLED", "1") == "1" {
			RunWorker(workerCtx, repo, &HTTPEventSender{Client: &http.Client{Timeout: 2 * time.Second}, URL: notifyURL}, time.Second, 2*time.Second)
		}
	}()
	signals, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	go func() {
		select {
		case <-signals.Done():
			lifecycle.RequestShutdown()
		case <-lifecycle.stopping:
		}
	}()
	log.Printf("database: %s; notification database: %s", dbPath, notificationPath)
	log.Printf("Go shutdown API listening at http://%s", listener.Addr())
	log.Printf("grace: %dms; worker enabled: %t", graceMS, envOr("WORKER_ENABLED", "1") == "1")
	report, serveErr := ServeUntilShutdown(listener, NewLifecycleHandler(lifecycle, business), lifecycle, time.Duration(graceMS)*time.Millisecond)
	<-workerDone
	// 资源关闭排在 handler 和 worker 均已结束之后。
	if err := notifications.Close(); err != nil {
		return err
	}
	if err := repo.Close(); err != nil {
		return err
	}
	log.Printf("shutdown complete: forced=%t active=%d; worker stopped; databases closed", report.Forced, lifecycle.State().ActiveRequests)
	return serveErr
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
