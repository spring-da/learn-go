package main

import (
	"context"
	"errors"
	"sync"
)

var ErrNameExists = errors.New("name already exists")

type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
	Create(ctx context.Context, name string) (User, error)
}

type MemoryUserRepository struct {
	mu     sync.RWMutex
	users  map[int64]User
	names  map[string]int64
	nextID int64
}

func NewMemoryUserRepository() *MemoryUserRepository {
	return &MemoryUserRepository{
		users:  map[int64]User{1: {ID: 1, Name: "Spring"}, 2: {ID: 2, Name: "Lin"}},
		names:  map[string]int64{"Spring": 1, "Lin": 2},
		nextID: 3,
	}
}

func (r *MemoryUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	if err := ctx.Err(); err != nil {
		return User{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	user, found := r.users[id]
	return user, found, nil
}

func (r *MemoryUserRepository) Create(ctx context.Context, name string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	// 检查唯一性和写入属于同一个临界区。
	if _, exists := r.names[name]; exists {
		return User{}, ErrNameExists
	}
	user := User{ID: r.nextID, Name: name}
	r.nextID++
	r.users[user.ID] = user
	r.names[name] = user.ID
	return user, nil
}
