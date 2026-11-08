package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestConcurrentNameUniqueness(t *testing.T) {
	repo := NewMemoryUserRepository()
	start := make(chan struct{})
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func() {
			<-start
			_, err := repo.Create(context.Background(), "Concurrent")
			results <- err
		}()
	}
	close(start)
	created := 0
	for i := 0; i < 32; i++ {
		err := <-results
		if err == nil {
			created++
		} else if !errors.Is(err, ErrNameExists) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d, want exactly 1", created)
	}
}

func TestConcurrentIDsAndReads(t *testing.T) {
	repo := NewMemoryUserRepository()
	results := make(chan User, 24)
	errorsFound := make(chan error, 24)
	for i := 0; i < 24; i++ {
		go func(index int) {
			user, err := repo.Create(context.Background(), fmt.Sprintf("user-%d", index))
			if err == nil {
				var found bool
				var fetched User
				fetched, found, err = repo.FindByID(context.Background(), user.ID)
				if err == nil && (!found || fetched != user) {
					err = fmt.Errorf("created user not readable: %+v", user)
				}
			}
			results <- user
			errorsFound <- err
		}(i)
	}
	ids := make(map[int64]bool)
	for i := 0; i < 24; i++ {
		user := <-results
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
		if ids[user.ID] || user.ID < 3 {
			t.Fatalf("invalid or duplicate ID: %d", user.ID)
		}
		ids[user.ID] = true
	}
}
