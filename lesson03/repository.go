package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type UserRepository interface {
	FindByID(id int64) (User, bool, error)
}

type FileUserRepository struct {
	Path string
}

func (r *FileUserRepository) FindByID(id int64) (User, bool, error) {
	data, err := os.ReadFile(r.Path)
	if err != nil {
		// %w 加上操作上下文，同时保留原始错误供调用方判断。
		return User{}, false, fmt.Errorf("read %s: %w", r.Path, err)
	}

	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return User{}, false, fmt.Errorf("decode %s: %w", r.Path, err)
	}

	for _, user := range users {
		if user.ID == id {
			return user, true, nil
		}
	}
	return User{}, false, nil
}

func Welcome(repo UserRepository, id int64) (string, error) {
	user, found, err := repo.FindByID(id)
	if err != nil {
		return "", fmt.Errorf("find user %d: %w", id, err)
	}
	if !found {
		return fmt.Sprintf("user %d not found", id), nil
	}
	return fmt.Sprintf("hello, %s", user.Name), nil
}
