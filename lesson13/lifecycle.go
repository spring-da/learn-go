package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type ServiceState struct {
	Draining       bool `json:"draining"`
	ActiveRequests int  `json:"active_requests"`
}

type Lifecycle struct {
	mu            sync.Mutex
	state         ServiceState
	stopping      chan struct{}
	drained       chan struct{}
	drainedClosed bool
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{stopping: make(chan struct{}), drained: make(chan struct{})}
}

func (l *Lifecycle) State() ServiceState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *Lifecycle) RequestShutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.state.Draining {
		l.state.Draining = true
		close(l.stopping)
	}
	l.closeDrainedIfReady()
}

func (l *Lifecycle) enter() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.Draining {
		return false
	}
	l.state.ActiveRequests++
	return true
}

func (l *Lifecycle) leave() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state.ActiveRequests--
	l.closeDrainedIfReady()
}

// 调用者持有 mu，接收请求与进入 draining 共用同一把锁。
func (l *Lifecycle) closeDrainedIfReady() {
	if l.state.Draining && l.state.ActiveRequests == 0 && !l.drainedClosed {
		l.drainedClosed = true
		close(l.drained)
	}
}

const invalidDelay = "X-Demo-Delay-Ms must be an integer from 0 to 10000"

func (l *Lifecycle) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.enter() {
			writeJSON(w, 503, ErrorBody{Error: "service is draining"})
			return
		}
		defer l.leave()
		values := r.Header.Values("X-Demo-Delay-Ms")
		if len(values) > 1 {
			writeJSON(w, 400, ErrorBody{Error: invalidDelay})
			return
		}
		if len(values) == 1 {
			milliseconds, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || milliseconds < 0 || milliseconds > 10000 {
				writeJSON(w, 400, ErrorBody{Error: invalidDelay})
				return
			}
			if milliseconds > 0 {
				log.Printf("request %s %s: demo delay %dms started", r.Method, r.URL.Path, milliseconds)
				timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					log.Printf("request %s %s: cancelled before business handler", r.Method, r.URL.Path)
					writeJSON(w, 503, ErrorBody{Error: "request cancelled during shutdown"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func NewLifecycleHandler(l *Lifecycle, business http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, l.State()) })
	mux.HandleFunc("POST /demo/shutdown", func(w http.ResponseWriter, r *http.Request) {
		l.RequestShutdown()
		writeJSON(w, 202, map[string]string{"status": "draining"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if l.State().Draining {
			writeJSON(w, 503, ErrorBody{Error: "service is draining"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.Handle("/", l.protect(business))
	return mux
}

type ShutdownReport struct{ Forced bool }

func ServeUntilShutdown(listener net.Listener, handler http.Handler, lifecycle *Lifecycle, grace time.Duration) (ShutdownReport, error) {
	// 此 context 只在宽限期耗尽时取消，不能直接使用收到退出信号就取消的 context。
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return requests }}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	var err error
	serveReturned := false
	select {
	case <-lifecycle.stopping:
	case err = <-serveErr:
		serveReturned = true
	}
	lifecycle.RequestShutdown()
	log.Printf("shutdown: draining active=%d grace=%s", lifecycle.State().ActiveRequests, grace)
	// Shutdown 立即关闭 listener，但保留已有请求；此等待使用新的、尚未取消的 context。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	report := ShutdownReport{Forced: errors.Is(shutdownErr, context.DeadlineExceeded)}
	if shutdownErr != nil {
		log.Printf("shutdown: grace ended, cancelling active requests: %v", shutdownErr)
		cancelRequests()
		_ = server.Close()
	}
	// Close 不会等待所有 handler 返回；显式等到本课的协作式 handler 清理完毕。
	<-lifecycle.drained
	if !serveReturned {
		err = <-serveErr
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return report, err
	}
	if shutdownErr != nil && !report.Forced {
		return report, shutdownErr
	}
	return report, nil
}
