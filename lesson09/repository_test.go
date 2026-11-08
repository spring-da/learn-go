package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTestRepository(t *testing.T, path string) *SQLUserRepository {
	t.Helper()
	repo, err := OpenUserRepository(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestPersistenceAndBoundParameters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "users with spaces.db")
	repo := openTestRepository(t, path)
	user, err := repo.Create(ctx, "O'Reilly")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestRepository(t, path)
	fetched, found, err := reopened.FindByID(ctx, user.ID)
	if err != nil || !found || fetched != user {
		t.Fatalf("persisted user=%+v found=%v err=%v", fetched, found, err)
	}
	if _, err := reopened.Create(ctx, "O'Reilly"); !errors.Is(err, ErrNameExists) {
		t.Fatalf("want name conflict after reopening, got %v", err)
	}
	if _, found, err := reopened.FindByID(ctx, user.ID+10000); err != nil || found {
		t.Fatalf("missing user: found=%v err=%v", found, err)
	}
}

func TestUniqueConstraintAcrossPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	repos := []*SQLUserRepository{openTestRepository(t, path), openTestRepository(t, path)}
	start := make(chan struct{})
	outcomes := make(chan error, 32)
	for index := 0; index < 32; index++ {
		go func(repo *SQLUserRepository) {
			<-start
			_, err := repo.Create(context.Background(), "Concurrent")
			outcomes <- err
		}(repos[index%len(repos)])
	}
	close(start)
	created := 0
	for index := 0; index < 32; index++ {
		err := <-outcomes
		if err == nil {
			created++
		} else if !errors.Is(err, ErrNameExists) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d, want exactly one across both pools", created)
	}
}
