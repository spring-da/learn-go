package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTransferRepository(t *testing.T, fromPoints, toPoints int64) (*SQLUserRepository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transfers.db")
	repo := openTestRepository(t, path)
	for _, user := range []struct {
		key, name string
		points    int64
	}{{"from-user", "Alice", fromPoints}, {"to-user", "Bob", toPoints}} {
		if _, err := repo.Create(context.Background(), user.key, user.name, user.points); err != nil {
			t.Fatal(err)
		}
	}
	return repo, path
}

func assertTransferState(t *testing.T, repo *SQLUserRepository, fromPoints, toPoints, fromVersion, toVersion int64, transfers int) {
	t.Helper()
	from, foundFrom, errFrom := repo.FindByID(context.Background(), 1)
	to, foundTo, errTo := repo.FindByID(context.Background(), 2)
	if errFrom != nil || errTo != nil || !foundFrom || !foundTo || from.Points != fromPoints || to.Points != toPoints || from.Version != fromVersion || to.Version != toVersion {
		t.Fatalf("unexpected accounts: from=%+v to=%+v errors=%v,%v", from, to, errFrom, errTo)
	}
	var count int
	if err := repo.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&count); err != nil || count != transfers {
		t.Fatalf("transfers=%d want=%d err=%v", count, transfers, err)
	}
	assertCounts(t, repo, 2, 2, 2)
}

func TestTransferHTTPReplayScopeAndVersions(t *testing.T) {
	repo, _ := newTransferRepository(t, 100, 20)
	handler := NewHandler(repo)
	// 与注册使用相同文本的 key，作用域由 /transfers 的独立表决定。
	body := `{"from_user_id":1,"to_user_id":2,"amount":30}`
	first := requestWithKey(handler, "POST", "/transfers", "application/json", body, "from-user")
	var receipt TransferReceipt
	if first.Code != 201 || first.Header().Get("Idempotency-Replayed") != "false" || first.Header().Get("Location") != "/transfers/1" || json.Unmarshal(first.Body.Bytes(), &receipt) != nil {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	if receipt != (TransferReceipt{ID: 1, FromUserID: 1, ToUserID: 2, Amount: 30, FromPoints: 70, ToPoints: 50, FromVersion: 1, ToVersion: 1}) {
		t.Fatalf("receipt=%+v", receipt)
	}
	replay := requestWithKey(handler, "POST", "/transfers", "application/json", `{"amount":30,"to_user_id":2,"from_user_id":1}`, "from-user")
	if replay.Code != 201 || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body)
	}
	fetched := request(handler, "GET", "/transfers/1", "", "")
	if fetched.Code != 200 || fetched.Body.String() != first.Body.String() {
		t.Fatal("receipt lookup changed response")
	}
	conflict := requestWithKey(handler, "POST", "/transfers", "application/json", `{"from_user_id":1,"to_user_id":2,"amount":31}`, "from-user")
	if conflict.Code != 409 {
		t.Fatalf("key mismatch=%d %s", conflict.Code, conflict.Body)
	}
	stale := requestWithKey(handler, "PUT", "/users/1/points", "application/json", `{"points":100,"expected_version":0}`, "")
	if stale.Code != 409 {
		t.Fatal("transfer did not advance account version")
	}
	assertTransferState(t, repo, 70, 50, 1, 1, 1)
}

func TestInvalidTransferRequestsDoNotChangeAccounts(t *testing.T) {
	repo, _ := newTransferRepository(t, 100, 20)
	handler := NewHandler(repo)
	valid := `{"from_user_id":1,"to_user_id":2,"amount":30}`
	cases := []struct {
		contentType, body, key string
		status                 int
	}{
		{"application/json", valid, "", 400},
		{"application/json", valid, "bad key", 400},
		{"", valid, "k", 415}, {"text/plain", valid, "k", 415},
		{"application/json", "null", "k", 400}, {"application/json", "[1,2,30]", "k", 400},
		{"application/json", "{}", "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2}`, "k", 400},
		{"application/json", `{"from_user_id":null,"to_user_id":2,"amount":30}`, "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":null}`, "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":"30"}`, "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":1.5}`, "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":9223372036854775808}`, "k", 400},
		{"application/json", valid + " {}", "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":30,"extra":1}`, "k", 400},
		{"application/json", `{"from_user_id":1,"to_user_id":1,"amount":30}`, "k", 422},
		{"application/json", `{"from_user_id":0,"to_user_id":2,"amount":30}`, "k", 422},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":0}`, "k", 422},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":-1}`, "k", 422},
		{"application/json", `{"from_user_id":1,"to_user_id":99,"amount":30}`, "k", 404},
		{"application/json", `{"from_user_id":1,"to_user_id":2,"amount":1000}`, "k", 409},
		{"application/json", strings.Repeat(" ", 1100) + valid, "k", 413},
	}
	for _, c := range cases {
		response := requestWithKey(handler, "POST", "/transfers", c.contentType, c.body, c.key)
		var body ErrorBody
		if response.Code != c.status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error == "" {
			t.Fatalf("body=%s status=%d want=%d response=%s", c.body, response.Code, c.status, response.Body)
		}
	}
	duplicate := httptest.NewRequest("POST", "/transfers", strings.NewReader(valid))
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Add("Idempotency-Key", "one")
	duplicate.Header.Add("Idempotency-Key", "two")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, duplicate)
	if response.Code != 400 {
		t.Fatal("multiple idempotency keys accepted")
	}
	for _, c := range []struct {
		path   string
		status int
	}{{"/transfers/nope", 400}, {"/transfers/0", 400}, {"/transfers/99", 404}, {"/transfers", 405}} {
		response := request(handler, "GET", c.path, "", "")
		if response.Code != c.status {
			t.Fatalf("GET %s=%d", c.path, response.Code)
		}
	}
	assertTransferState(t, repo, 100, 20, 0, 0, 0)
}

func TestTransferSnapshotPersistsAfterOtherUpdates(t *testing.T) {
	ctx := context.Background()
	repo, path := newTransferRepository(t, 100, 20)
	first, err := repo.Transfer(ctx, "persist-transfer", 1, 2, 30)
	if err != nil || first.Replayed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := repo.ReplacePoints(ctx, 1, 80, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Transfer(ctx, "other-transfer", 2, 1, 5); err != nil {
		t.Fatal(err)
	}
	repo.Close()
	reopened := openTestRepository(t, path)
	replay, err := reopened.Transfer(ctx, "persist-transfer", 1, 2, 30)
	if err != nil || !replay.Replayed || replay.Transfer != first.Transfer {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	for _, args := range [][3]int64{{1, 2, 31}, {2, 1, 30}} {
		if _, err := reopened.Transfer(ctx, "persist-transfer", args[0], args[1], args[2]); !errors.Is(err, ErrTransferKeyMismatch) {
			t.Fatalf("key conflict=%v", err)
		}
	}
	assertTransferState(t, reopened, 85, 45, 3, 2, 2)
}

func TestTransferCreditAndReceiptFailuresRollbackBothAccounts(t *testing.T) {
	triggers := map[string]string{
		"credit":  `CREATE TRIGGER reject_write BEFORE UPDATE ON points_accounts WHEN NEW.user_id = 2 BEGIN SELECT RAISE(ABORT, 'simulated credit failure'); END`,
		"receipt": `CREATE TRIGGER reject_write BEFORE INSERT ON transfers BEGIN SELECT RAISE(ABORT, 'simulated receipt failure'); END`,
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			repo, _ := newTransferRepository(t, 100, 20)
			if _, err := repo.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Transfer(context.Background(), "retry-key", 1, 2, 30); err == nil {
				t.Fatal("expected SQL failure")
			}
			assertTransferState(t, repo, 100, 20, 0, 0, 0)
			if _, err := repo.db.Exec("DROP TRIGGER reject_write"); err != nil {
				t.Fatal(err)
			}
			if result, err := repo.Transfer(context.Background(), "retry-key", 1, 2, 30); err != nil || result.Replayed {
				t.Fatalf("retry=%+v err=%v", result, err)
			}
			assertTransferState(t, repo, 70, 50, 1, 1, 1)
		})
	}
}

func TestConcurrentSameTransferKeyAcrossPools(t *testing.T) {
	repo, path := newTransferRepository(t, 100, 20)
	other := openTestRepository(t, path)
	start := make(chan struct{})
	type outcome struct {
		result TransferResult
		err    error
	}
	results := make(chan outcome, 32)
	for i := 0; i < 32; i++ {
		current := repo
		if i%2 != 0 {
			current = other
		}
		go func() {
			<-start
			result, err := current.Transfer(context.Background(), "same-key", 1, 2, 30)
			results <- outcome{result, err}
		}()
	}
	close(start)
	created, replayed := 0, 0
	var expected TransferReceipt
	for i := 0; i < 32; i++ {
		outcome := <-results
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if i == 0 {
			expected = outcome.result.Transfer
		} else if outcome.result.Transfer != expected {
			t.Fatal("replays differ")
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
	assertTransferState(t, repo, 70, 50, 1, 1, 1)
}

func TestConcurrentDistinctTransferKeysCannotOverdraw(t *testing.T) {
	repo, path := newTransferRepository(t, 100, 0)
	other := openTestRepository(t, path)
	start := make(chan struct{})
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		current := repo
		if i%2 != 0 {
			current = other
		}
		go func(key string) {
			<-start
			_, err := current.Transfer(context.Background(), key, 1, 2, 10)
			results <- err
		}(fmt.Sprintf("transfer-%d", i))
	}
	close(start)
	accepted, rejected := 0, 0
	for i := 0; i < 32; i++ {
		err := <-results
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrInsufficientPoints) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 10 || rejected != 22 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	assertTransferState(t, repo, 0, 100, 10, 10, 10)
}

func TestOppositeTransfersConservePoints(t *testing.T) {
	repo, path := newTransferRepository(t, 100, 100)
	other := openTestRepository(t, path)
	start := make(chan struct{})
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		current, from, to := repo, int64(1), int64(2)
		if i%2 != 0 {
			current, from, to = other, 2, 1
		}
		go func(key string) {
			<-start
			_, err := current.Transfer(context.Background(), key, from, to, 1)
			results <- err
		}(fmt.Sprintf("opposite-%d", i))
	}
	close(start)
	for i := 0; i < 32; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertTransferState(t, repo, 100, 100, 32, 32, 32)
}

func TestTransferBoundsAndFailedKeyReuse(t *testing.T) {
	ctx := context.Background()
	t.Run("validation and failure do not reserve keys", func(t *testing.T) {
		repo, _ := newTransferRepository(t, 100, 20)
		for _, args := range [][3]int64{{0, 2, 10}, {1, 1, 10}, {1, 2, 0}, {1, 2, -1}} {
			if _, err := repo.Transfer(ctx, "bad", args[0], args[1], args[2]); !errors.Is(err, ErrInvalidTransfer) {
				t.Fatalf("invalid args=%v err=%v", args, err)
			}
		}
		if _, err := repo.Transfer(ctx, "missing", 1, 99, 10); !errors.Is(err, ErrUserNotFound) {
			t.Fatal(err)
		}
		if _, err := repo.Transfer(ctx, "insufficient", 1, 2, 1000); !errors.Is(err, ErrInsufficientPoints) {
			t.Fatal(err)
		}
		assertTransferState(t, repo, 100, 20, 0, 0, 0)
		if result, err := repo.Transfer(ctx, "insufficient", 1, 2, 10); err != nil || result.Replayed {
			t.Fatalf("retry=%+v err=%v", result, err)
		}
		assertTransferState(t, repo, 90, 30, 1, 1, 1)
	})
	t.Run("destination overflow", func(t *testing.T) {
		repo, _ := newTransferRepository(t, 100, math.MaxInt64)
		if _, err := repo.Transfer(ctx, "overflow", 1, 2, 1); !errors.Is(err, ErrPointsOverflow) {
			t.Fatal(err)
		}
		assertTransferState(t, repo, 100, math.MaxInt64, 0, 0, 0)
	})
	t.Run("version limit", func(t *testing.T) {
		repo, _ := newTransferRepository(t, 100, 20)
		if _, err := repo.db.Exec("UPDATE points_accounts SET version = ? WHERE user_id = 1", int64(math.MaxInt64)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Transfer(ctx, "version-limit", 1, 2, 1); !errors.Is(err, ErrVersionExhausted) {
			t.Fatal(err)
		}
		assertTransferState(t, repo, 100, 20, math.MaxInt64, 0, 0)
	})
	t.Run("largest integer amount", func(t *testing.T) {
		repo, _ := newTransferRepository(t, math.MaxInt64, 0)
		if _, err := repo.Transfer(ctx, "max", 1, 2, math.MaxInt64); err != nil {
			t.Fatal(err)
		}
		assertTransferState(t, repo, 0, math.MaxInt64, 1, 1, 1)
	})
}
