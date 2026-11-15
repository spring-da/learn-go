package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schema string

var (
	ErrNameExists    = errors.New("name already exists")
	ErrInvalidPoints = errors.New("initial points must be non-negative")
	ErrKeyMismatch   = errors.New("idempotency key was used with different parameters")
)

type User struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Points  int64  `json:"points"`
	Version int64  `json:"version"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
	Create(ctx context.Context, key, name string, initialPoints int64) (RegistrationResult, error)
	ListOutbox(ctx context.Context) ([]OutboxItem, error)
	ReplacePoints(ctx context.Context, id, points, expectedVersion int64) (PointsAccount, error)
}

type RegistrationResult struct {
	User     User
	Replayed bool
}

type SQLUserRepository struct {
	db *sql.DB
}

func OpenUserRepository(ctx context.Context, path string) (*SQLUserRepository, error) {
	db, err := openDatabase(ctx, path, schema)
	if err != nil {
		return nil, err
	}
	return &SQLUserRepository{db: db}, nil
}

func openDatabase(ctx context.Context, path, ddl string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath}
	params := url.Values{}
	params.Set("_txlock", "immediate")
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return db, nil
}

func (r *SQLUserRepository) Close() error {
	return r.db.Close()
}

func (r *SQLUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	var user User
	err := r.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, a.points, a.version
		FROM users AS u JOIN points_accounts AS a ON a.user_id = u.id
		WHERE u.id = ?`, id).Scan(&user.ID, &user.Name, &user.Points, &user.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	return user, true, nil
}

func (r *SQLUserRepository) Create(ctx context.Context, key, name string, initialPoints int64) (RegistrationResult, error) {
	// DSN 的 _txlock=immediate 让此事务在读取幂等记录前先取得写入资格。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("begin registration: %w", err)
	}
	defer tx.Rollback()

	var user User
	var savedName string
	var savedPoints int64
	err = tx.QueryRowContext(ctx, `
		SELECT request_name, request_points, user_id, response_name, response_points, response_version
		FROM idempotency_records WHERE idempotency_key = ?`, key,
	).Scan(&savedName, &savedPoints, &user.ID, &user.Name, &user.Points, &user.Version)
	if err == nil {
		if savedName != name || savedPoints != initialPoints {
			return RegistrationResult{}, ErrKeyMismatch
		}
		if err := tx.Commit(); err != nil {
			return RegistrationResult{}, fmt.Errorf("finish replay: %w", err)
		}
		return RegistrationResult{User: user, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RegistrationResult{}, fmt.Errorf("read idempotency record: %w", err)
	}

	err = tx.QueryRowContext(ctx,
		"INSERT INTO users (name) VALUES (?) RETURNING id, name", name,
	).Scan(&user.ID, &user.Name)
	if err != nil {
		return RegistrationResult{}, classifyWriteError("create user", err)
	}
	err = tx.QueryRowContext(ctx,
		"INSERT INTO points_accounts (user_id, points) VALUES (?, ?) RETURNING points, version", user.ID, initialPoints,
	).Scan(&user.Points, &user.Version)
	if err != nil {
		return RegistrationResult{}, classifyWriteError("create points account", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO idempotency_records
		(idempotency_key, request_name, request_points, user_id, response_name, response_points, response_version)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, key, name, initialPoints, user.ID, user.Name, user.Points, user.Version)
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("save idempotency record: %w", err)
	}
	// 事件与业务、请求幂等记录一起提交。重放分支不会再次插入事件。
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO outbox_events (user_id, name, points) VALUES (?, ?, ?)", user.ID, user.Name, user.Points,
	); err != nil {
		return RegistrationResult{}, fmt.Errorf("save outbox event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegistrationResult{}, fmt.Errorf("commit registration: %w", err)
	}
	return RegistrationResult{User: user}, nil
}

func classifyWriteError(operation string, err error) error {
	var databaseError *sqlite.Error
	if errors.As(err, &databaseError) {
		switch databaseError.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return ErrNameExists
		case sqlite3.SQLITE_CONSTRAINT_CHECK:
			return ErrInvalidPoints
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
