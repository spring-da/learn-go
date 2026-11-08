package main

import (
	"fmt"
	"time"
)

func main() {
	repo := NewMemoryUserRepository()
	ids := []int64{1, 2, 1, 404, 500}

	for _, workers := range []int{1, 2} {
		start := time.Now()
		results, err := FindWithPool(repo, ids, workers)
		if err != nil {
			fmt.Println("pool error:", err)
			return
		}
		fmt.Printf("workers=%d count=%d elapsed=%dms\n", workers, len(results), time.Since(start).Milliseconds())
		for _, result := range results {
			switch {
			case result.Err != nil:
				fmt.Printf("  index=%d id=%d error=%v\n", result.Index, result.ID, result.Err)
			case !result.Found:
				fmt.Printf("  index=%d id=%d not-found\n", result.Index, result.ID)
			default:
				fmt.Printf("  index=%d id=%d found=%s\n", result.Index, result.ID, result.User.Name)
			}
		}
	}

	empty, err := FindWithPool(repo, nil, 2)
	if err != nil {
		fmt.Println("pool error:", err)
		return
	}
	fmt.Printf("empty: count=%d\n", len(empty))

	if _, err := FindWithPool(repo, ids, 0); err != nil {
		fmt.Println("invalid workers=0:", err)
	}
}
