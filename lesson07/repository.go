package main

import (
	"context"
	"errors"
	"time"
)

type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserRepository interface {
	FindByID(ctx context.Context, id int64) (User, bool, error)
}

type MemoryUserRepository struct {
	users map[int64]User
}

func NewMemoryUserRepository() *MemoryUserRepository {
	return &MemoryUserRepository{
		users: map[int64]User{
			1: {ID: 1, Name: "Spring"},
			2: {ID: 2, Name: "Lin"},
		},
	}
}

func (r *MemoryUserRepository) FindByID(ctx context.Context, id int64) (User, bool, error) {
	if err := ctx.Err(); err != nil {
		return User{}, false, err
	}
	delay := 20 * time.Millisecond
	if id == 99 {
		delay = 600 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return User{}, false, ctx.Err()
	case <-timer.C:
	}
	if err := ctx.Err(); err != nil {
		return User{}, false, err
	}
	if id == 500 {
		return User{}, false, errors.New("simulated storage connection failure")
	}
	user, found := r.users[id]
	return user, found, nil
}
