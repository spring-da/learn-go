package main

import (
	"context"
	"errors"
	"sort"
	"sync"
)

type Job struct {
	Index int
	ID    int64
}

type LookupResult struct {
	Index int
	ID    int64
	User  User
	Found bool
	Err   error
}

func FindWithPool(ctx context.Context, repo UserRepository, ids []int64, workers int) ([]LookupResult, error) {
	if workers <= 0 {
		return nil, errors.New("workers must be greater than zero")
	}
	if len(ids) == 0 {
		return []LookupResult{}, nil
	}
	if workers > len(ids) {
		workers = len(ids)
	}

	jobs := make(chan Job, workers)
	results := make(chan LookupResult)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				user, found, err := repo.FindByID(ctx, job.ID)
				// 完整交回每条结果，由调用方持续接收。
				results <- LookupResult{
					Index: job.Index, ID: job.ID,
					User: user, Found: found, Err: err,
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index, id := range ids {
			jobs <- Job{Index: index, ID: id}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	all := make([]LookupResult, 0, len(ids))
	for result := range results {
		all = append(all, result)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Index < all[j].Index
	})
	return all, nil
}
