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

var ErrNameExists = errors.New("name already exists")

type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
	Create(ctx context.Context, name string) (User, error)
}

type SQLUserRepository struct {
	db *sql.DB
}

// 调用方负责先创建数据库文件的父目录。
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
	// WAL 是数据库文件的持久设置；连接级设置放在上面的 DSN 中。
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return &SQLUserRepository{db: db}, nil
}

func (r *SQLUserRepository) Close() error {
	return r.db.Close()
}

func (r *SQLUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	var user User
	err := r.db.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", id).Scan(&user.ID, &user.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("find user: %w", err)
	}
	return user, true, nil
}

func (r *SQLUserRepository) Create(ctx context.Context, name string) (User, error) {
	var user User
	err := r.db.QueryRowContext(ctx,
		"INSERT INTO users (name) VALUES (?) RETURNING id, name", name,
	).Scan(&user.ID, &user.Name)
	if err != nil {
		var databaseError *sqlite.Error
		if errors.As(err, &databaseError) && databaseError.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return User{}, ErrNameExists
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}
