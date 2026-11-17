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
)

type User struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Points int64  `json:"points"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
	Create(ctx context.Context, name string, initialPoints int64) (User, error)
}

type SQLUserRepository struct {
	db *sql.DB
}

func OpenUserRepository(ctx context.Context, path string) (*SQLUserRepository, error) {
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
		err := db.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		err := db.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return &SQLUserRepository{db: db}, nil
}

func (r *SQLUserRepository) Close() error {
	return r.db.Close()
}

func (r *SQLUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	var user User
	err := r.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, a.points
		FROM users AS u JOIN points_accounts AS a ON a.user_id = u.id
		WHERE u.id = ?`, id).Scan(&user.ID, &user.Name, &user.Points)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	return user, true, nil
}

func (r *SQLUserRepository) Create(ctx context.Context, name string, initialPoints int64) (User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("begin registration: %w", err)
	}
	// 任意错误分支提前返回，都会执行回滚；Commit 成功后此调用无需再做工作。
	defer func(tx *sql.Tx) {
		err := tx.Rollback()
		if err != nil {
		}
	}(tx)

	var user User
	err = tx.QueryRowContext(ctx,
		"INSERT INTO users (name) VALUES (?) RETURNING id, name", name,
	).Scan(&user.ID, &user.Name)
	if err != nil {
		return User{}, classifyWriteError("create user", err)
	}
	// 故意将非负规则放在数据库 CHECK 中，负积分会在第二条 SQL 处失败。
	err = tx.QueryRowContext(ctx,
		"INSERT INTO points_accounts (user_id, points) VALUES (?, ?) RETURNING points", user.ID, initialPoints,
	).Scan(&user.Points)
	if err != nil {
		return User{}, classifyWriteError("create points account", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit registration: %w", err)
	}
	return user, nil
}

func classifyWriteError(operation string, err error) error {
	if databaseError, ok := errors.AsType[*sqlite.Error](err); ok {
		switch databaseError.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return ErrNameExists
		case sqlite3.SQLITE_CONSTRAINT_CHECK:
			return ErrInvalidPoints
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
