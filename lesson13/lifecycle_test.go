package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testService struct {
	lifecycle *Lifecycle
	url       string
	done      chan struct{}
	report    ShutdownReport
	err       error
}

func startTestService(t *testing.T, repo *SQLUserRepository, grace time.Duration) *testService {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &testService{lifecycle: NewLifecycle(), url: "http://" + listener.Addr().String(), done: make(chan struct{})}
	go func() {
		defer close(service.done)
		service.report, service.err = ServeUntilShutdown(listener, NewLifecycleHandler(service.lifecycle, NewHandler(repo)), service.lifecycle, grace)
	}()
	t.Cleanup(func() { service.lifecycle.RequestShutdown(); service.wait(t) })
	return service
}

func (s *testService) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
		if s.err != nil {
			t.Fatal(s.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("service did not finish shutdown")
	}
}

func waitActive(t *testing.T, lifecycle *Lifecycle, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lifecycle.State().ActiveRequests == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active requests=%d, want %d", lifecycle.State().ActiveRequests, want)
}

func TestDrainingRejectsNewWorkAndReadiness(t *testing.T) {
	lifecycle := NewLifecycle()
	if !lifecycle.enter() {
		t.Fatal("running service refused work")
	}
	called := false
	handler := NewLifecycleHandler(lifecycle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 200 {
		t.Fatal("service should be ready")
	}
	lifecycle.RequestShutdown()
	lifecycle.RequestShutdown()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/users", nil))
	if response.Code != 503 || called {
		t.Fatal("draining service admitted new business work")
	}
	ready = httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 503 {
		t.Fatal("draining service still reports ready")
	}
	select {
	case <-lifecycle.drained:
		t.Fatal("active request was not awaited")
	default:
	}
	lifecycle.leave()
	select {
	case <-lifecycle.drained:
	default:
		t.Fatal("drain completion was not signalled")
	}
}

type clientResult struct {
	response *http.Response
	err      error
}

func delayedRegistration(url string, milliseconds int) <-chan clientResult {
	result := make(chan clientResult, 1)
	go func() {
		request, _ := http.NewRequest("POST", url+"/users", strings.NewReader(`{"name":"Graceful","initial_points":100}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "shutdown-key")
		request.Header.Set("X-Demo-Delay-Ms", fmt.Sprint(milliseconds))
		response, err := (&http.Client{Timeout: 4 * time.Second}).Do(request)
		result <- clientResult{response: response, err: err}
	}()
	return result
}

func TestGracefulShutdownCompletesRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graceful.db")
	repo := openTestRepository(t, path)
	service := startTestService(t, repo, 2*time.Second)
	result := delayedRegistration(service.url, 200)
	waitActive(t, service.lifecycle, 1)
	response, err := (&http.Client{Timeout: time.Second}).Post(service.url+"/demo/shutdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("shutdown status=%d", response.StatusCode)
	}
	created := <-result
	if created.err != nil {
		t.Fatal(created.err)
	}
	io.Copy(io.Discard, created.response.Body)
	created.response.Body.Close()
	if created.response.StatusCode != 201 {
		t.Fatalf("registration status=%d", created.response.StatusCode)
	}
	service.wait(t)
	if service.report.Forced || service.lifecycle.State().ActiveRequests != 0 {
		t.Fatal("request should have completed during grace")
	}
	assertCounts(t, repo, 1, 1, 1)
	repo.Close()
	reopened := openTestRepository(t, path)
	assertCounts(t, reopened, 1, 1, 1)
	if oneOutbox(t, reopened).Status != "pending" {
		t.Fatal("shutdown must preserve unprocessed outbox event")
	}
}

func TestGraceDeadlineCancelsDelayedRequestWithoutWrites(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "forced.db"))
	service := startTestService(t, repo, 50*time.Millisecond)
	result := delayedRegistration(service.url, 2000)
	waitActive(t, service.lifecycle, 1)
	start := time.Now()
	service.lifecycle.RequestShutdown()
	service.wait(t)
	if !service.report.Forced || time.Since(start) >= time.Second {
		t.Fatal("deadline did not cancel delayed request promptly")
	}
	finished := <-result
	if finished.response != nil {
		finished.response.Body.Close()
		if finished.response.StatusCode >= 200 && finished.response.StatusCode < 300 {
			t.Fatal("cancelled registration reported success")
		}
	}
	if service.lifecycle.State().ActiveRequests != 0 {
		t.Fatal("request cleanup is still running")
	}
	assertCounts(t, repo, 0, 0, 0)
}

func TestGraceDeadlineCancelsIncompleteRequestBody(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "body.db"))
	service := startTestService(t, repo, 50*time.Millisecond)
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(service.url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = fmt.Fprintf(connection, "POST /users HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nIdempotency-Key: body-key\r\nContent-Length: 64\r\n\r\n{\"name\":")
	if err != nil {
		t.Fatal(err)
	}
	waitActive(t, service.lifecycle, 1)
	service.lifecycle.RequestShutdown()
	service.wait(t)
	if !service.report.Forced || service.lifecycle.State().ActiveRequests != 0 {
		t.Fatal("incomplete body prevented cancellation")
	}
	assertCounts(t, repo, 0, 0, 0)
}

func TestInvalidDemoDelayDoesNotEnterBusinessHandler(t *testing.T) {
	lifecycle := NewLifecycle()
	called := false
	handler := NewLifecycleHandler(lifecycle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	for _, value := range []string{"", "bad", "-1", "10001"} {
		request := httptest.NewRequest("POST", "/users", nil)
		request.Header.Set("X-Demo-Delay-Ms", value)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 400 || called || lifecycle.State().ActiveRequests != 0 {
			t.Fatalf("invalid delay=%q status=%d", value, response.Code)
		}
	}
	request := httptest.NewRequest("POST", "/users", nil).WithContext(context.Background())
	request.Header.Add("X-Demo-Delay-Ms", "1")
	request.Header.Add("X-Demo-Delay-Ms", "2")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 400 || called {
		t.Fatal("duplicate delay header was accepted")
	}
}
