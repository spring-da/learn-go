package main

import (
	"fmt"
	"time"
)

func printResults(label string, results []LookupResult, elapsed time.Duration) {
	fmt.Printf("%s: count=%d elapsed=%dms\n", label, len(results), elapsed.Milliseconds())
	for _, result := range results {
		switch {
		case result.Err != nil:
			fmt.Printf("  id=%d error=%v\n", result.ID, result.Err)
		case !result.Found:
			fmt.Printf("  id=%d not-found\n", result.ID)
		default:
			fmt.Printf("  id=%d found=%s\n", result.ID, result.User.Name)
		}
	}
}

func main() {
	repo := NewMemoryUserRepository()
	ids := []int64{1, 2, 404, 500}

	start := time.Now()
	sequential := FindSequentially(repo, ids)
	printResults("sequential", sequential, time.Since(start))

	start = time.Now()
	concurrent := FindConcurrently(repo, ids)
	printResults("concurrent", concurrent, time.Since(start))
}
