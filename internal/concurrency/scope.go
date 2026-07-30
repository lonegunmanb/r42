package concurrency

import (
	"context"
	"fmt"
	"slices"
)

const DefaultGlobalParallelism = 10

type semaphore chan struct{}

type Scope struct {
	semaphores []semaphore
	limit      int
}

func NewScope(globalParallelism int) (*Scope, error) {
	if globalParallelism < 0 {
		return nil, fmt.Errorf("global parallelism must not be negative")
	}
	if globalParallelism == 0 {
		globalParallelism = DefaultGlobalParallelism
	}
	return &Scope{
		semaphores: []semaphore{make(semaphore, globalParallelism)},
		limit:      globalParallelism,
	}, nil
}

func (s *Scope) Module(parallelism int) (*Scope, error) {
	if s == nil {
		return nil, fmt.Errorf("parent concurrency scope is required")
	}
	if parallelism < 0 {
		return nil, fmt.Errorf("module parallelism must not be negative")
	}
	result := &Scope{semaphores: slices.Clone(s.semaphores), limit: s.limit}
	if parallelism > 0 {
		result.semaphores = append(result.semaphores, make(semaphore, parallelism))
		result.limit = min(result.limit, parallelism)
	}
	return result, nil
}

func (s *Scope) Limit() int {
	if s == nil {
		return 0
	}
	return s.limit
}

func (s *Scope) WithResearch(ctx context.Context, work func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("concurrency scope is required")
	}
	if work == nil {
		return fmt.Errorf("research work is required")
	}
	acquired := 0
	defer func() {
		for index := acquired - 1; index >= 0; index-- {
			<-s.semaphores[index]
		}
	}()
	for _, semaphore := range s.semaphores {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case semaphore <- struct{}{}:
			acquired++
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return work(ctx)
}
