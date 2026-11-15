package main

import (
	"context"
	"errors"
	"fmt"
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

func assertCounts(t *testing.T, repo *SQLUserRepository, users, accounts, records int) {
	t.Helper()
	var u, a, r int
	err := repo.db.QueryRowContext(context.Background(), `SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(*) FROM points_accounts),
		(SELECT COUNT(*) FROM idempotency_records)`).Scan(&u, &a, &r)
	if err != nil || u != users || a != accounts || r != records {
		t.Fatalf("counts=(%d,%d,%d), want=(%d,%d,%d), err=%v", u, a, r, users, accounts, records, err)
	}
}

func TestReplayPersistsAndUsesOriginalSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	repo := openTestRepository(t, path)
	first, err := repo.Create(ctx, "persist-key", "O'Reilly", 100)
	if err != nil || first.Replayed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := repo.db.ExecContext(ctx, "UPDATE points_accounts SET points = 150 WHERE user_id = ?", first.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestRepository(t, path)
	replay, err := reopened.Create(ctx, "persist-key", "O'Reilly", 100)
	if err != nil || !replay.Replayed || replay.User != first.User {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	live, found, err := reopened.FindByID(ctx, first.User.ID)
	if err != nil || !found || live.Points != 150 {
		t.Fatalf("live user=%+v found=%v err=%v", live, found, err)
	}
	if _, err := reopened.Create(ctx, "persist-key", "O'Reilly", 101); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("want key mismatch, got %v", err)
	}
	if _, err := reopened.Create(ctx, "another-key", "O'Reilly", 100); !errors.Is(err, ErrNameExists) {
		t.Fatalf("want name conflict, got %v", err)
	}
	assertCounts(t, reopened, 1, 1, 1)
}

func TestFailureDoesNotReserveKey(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "retry.db"))
	ctx := context.Background()
	if _, err := repo.Create(ctx, "retry-key", "Recoverable", -1); !errors.Is(err, ErrInvalidPoints) {
		t.Fatalf("want points error, got %v", err)
	}
	assertCounts(t, repo, 0, 0, 0)
	first, err := repo.Create(ctx, "retry-key", "Recoverable", 50)
	if err != nil || first.Replayed {
		t.Fatalf("corrected retry=%+v err=%v", first, err)
	}
	replay, err := repo.Create(ctx, "retry-key", "Recoverable", 50)
	if err != nil || !replay.Replayed || replay.User != first.User {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	assertCounts(t, repo, 1, 1, 1)
}

func TestIdempotencyWriteFailureRollsBackBusinessWrites(t *testing.T) {
	repo := openTestRepository(t, filepath.Join(t.TempDir(), "cache-failure.db"))
	ctx := context.Background()
	_, err := repo.db.ExecContext(ctx, `CREATE TRIGGER reject_cache BEFORE INSERT ON idempotency_records
		WHEN NEW.idempotency_key = 'fail-cache'
		BEGIN SELECT RAISE(ABORT, 'simulated idempotency write failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, "fail-cache", "Atomic", 100); err == nil {
		t.Fatal("third write must fail")
	}
	assertCounts(t, repo, 0, 0, 0)
	if _, err := repo.db.ExecContext(ctx, "DROP TRIGGER reject_cache"); err != nil {
		t.Fatal(err)
	}
	result, err := repo.Create(ctx, "fail-cache", "Atomic", 100)
	if err != nil || result.Replayed {
		t.Fatalf("retry=%+v err=%v", result, err)
	}
	assertCounts(t, repo, 1, 1, 1)
}

func TestConcurrentSameKeyAcrossPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same-key.db")
	repos := []*SQLUserRepository{openTestRepository(t, path), openTestRepository(t, path)}
	type outcome struct {
		result RegistrationResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 32)
	for index := 0; index < 32; index++ {
		go func(repo *SQLUserRepository) {
			<-start
			result, err := repo.Create(context.Background(), "same-key", "Concurrent", 100)
			outcomes <- outcome{result, err}
		}(repos[index%2])
	}
	close(start)
	created, replayed := 0, 0
	var expected User
	for index := 0; index < 32; index++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if index == 0 {
			expected = outcome.result.User
		}
		if outcome.result.User != expected {
			t.Fatal("retries returned different users")
		}
		if outcome.result.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 31 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}
	assertCounts(t, repos[0], 1, 1, 1)
}

func TestDifferentKeysStillRespectNameUniqueness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "different-keys.db")
	repos := []*SQLUserRepository{openTestRepository(t, path), openTestRepository(t, path)}
	outcomes := make(chan error, 16)
	for index := 0; index < 16; index++ {
		go func(index int) {
			_, err := repos[index%2].Create(context.Background(), fmt.Sprintf("key-%d", index), "SameName", 100)
			outcomes <- err
		}(index)
	}
	created := 0
	for index := 0; index < 16; index++ {
		err := <-outcomes
		if err == nil {
			created++
		} else if !errors.Is(err, ErrNameExists) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	assertCounts(t, repos[0], 1, 1, 1)
}
