package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Handler:           NewHandler(NewMemoryUserRepository()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Go user API listening at http://%s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
