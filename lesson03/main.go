package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

func errorKind(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "missing-file"
	}

	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return "invalid-json"
	}
	return "repository-error"
}

func main() {
	cases := []struct {
		name string
		path string
		id   int64
	}{
		{name: "found", path: "data/users.json", id: 1},
		{name: "absent", path: "data/users.json", id: 404},
		{name: "missing-file", path: "data/missing.json", id: 1},
		{name: "invalid-json", path: "data/broken.json", id: 1},
		{name: "found-again", path: "data/users.json", id: 2},
	}

	for _, c := range cases {
		repo := &FileUserRepository{Path: c.path}
		message, err := Welcome(repo, c.id)
		if err != nil {
			fmt.Printf("case=%s error=%s\n", c.name, errorKind(err))
			fmt.Printf("  detail: %v\n", err)
			continue
		}
		fmt.Printf("case=%s result=%s\n", c.name, message)
	}
}
