package concurrency_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestDefaultGlobalParallelismIsTen(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(0)
	require.NoError(t, err)
	assert.Equal(t, 10, scope.Limit())
	assert.Equal(t, 10, maximumConcurrent(t, scope, 11))
}

func TestNestedModuleCapsIntersect(t *testing.T) {
	t.Parallel()

	root, err := r42concurrency.NewScope(4)
	require.NoError(t, err)
	parent, err := root.Module(3)
	require.NoError(t, err)
	child, err := parent.Module(2)
	require.NoError(t, err)

	assert.Equal(t, 2, maximumConcurrent(t, child, 8))
}

func TestModuleWithoutExplicitCapUsesAncestorLimitsAndConsumesNoSlot(t *testing.T) {
	t.Parallel()

	root, err := r42concurrency.NewScope(2)
	require.NoError(t, err)
	module, err := root.Module(0)
	require.NoError(t, err)

	assert.Equal(t, 2, module.Limit())
	assert.Equal(t, 2, maximumConcurrent(t, module, 4))
}

func TestScopeValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "negative global parallelism",
			run: func() error {
				_, err := r42concurrency.NewScope(-1)
				return err
			},
			want: "global parallelism must not be negative",
		},
		{
			name: "nil parent scope",
			run: func() error {
				var scope *r42concurrency.Scope
				_, err := scope.Module(1)
				return err
			},
			want: "parent concurrency scope is required",
		},
		{
			name: "negative module parallelism",
			run: func() error {
				scope, err := r42concurrency.NewScope(1)
				if err != nil {
					return err
				}
				_, err = scope.Module(-1)
				return err
			},
			want: "module parallelism must not be negative",
		},
		{
			name: "nil scope research",
			run: func() error {
				var scope *r42concurrency.Scope
				return scope.WithResearch(t.Context(), func(context.Context) error { return nil })
			},
			want: "concurrency scope is required",
		},
		{
			name: "nil research callback",
			run: func() error {
				scope, err := r42concurrency.NewScope(1)
				if err != nil {
					return err
				}
				return scope.WithResearch(t.Context(), nil)
			},
			want: "research work is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.EqualError(t, tt.run(), tt.want)
		})
	}
}

func TestNilScopeLimitIsZero(t *testing.T) {
	t.Parallel()

	var scope *r42concurrency.Scope
	assert.Zero(t, scope.Limit())
}

func TestWithResearchHonorsCanceledContextAndCallbackError(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err = scope.WithResearch(canceled, func(context.Context) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)

	wantErr := errors.New("research failed")
	err = scope.WithResearch(t.Context(), func(context.Context) error { return wantErr })
	assert.ErrorIs(t, err, wantErr)
}

func TestCanceledLeafAcquisitionReleasesAlreadyAcquiredAncestors(t *testing.T) {
	t.Parallel()

	root, err := r42concurrency.NewScope(2)
	require.NoError(t, err)
	module, err := root.Module(1)
	require.NoError(t, err)
	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- module.WithResearch(t.Context(), func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()
	<-firstStarted

	ctx, cancel := context.WithCancel(t.Context())
	secondAttempted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondAttempted)
		secondDone <- module.WithResearch(ctx, func(context.Context) error { return nil })
	}()
	<-secondAttempted
	cancel()
	require.ErrorIs(t, <-secondDone, context.Canceled)

	rootOnlyDone := make(chan error, 1)
	go func() { rootOnlyDone <- root.WithResearch(t.Context(), func(context.Context) error { return nil }) }()
	require.NoError(t, <-rootOnlyDone)
	close(releaseFirst)
	require.NoError(t, <-firstDone)
}

func TestResearchPermitCoversQCAndClose(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	events := make(chan string, 4)
	firstDone := make(chan error, 1)
	closeAllowed := make(chan struct{})
	go func() {
		firstDone <- scope.WithResearch(t.Context(), func(context.Context) error {
			events <- "research"
			events <- "qc"
			<-closeAllowed
			events <- "close"
			return nil
		})
	}()
	assert.Equal(t, "research", <-events)
	assert.Equal(t, "qc", <-events)
	secondStarted := make(chan struct{})
	secondAttempted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondAttempted)
		secondDone <- scope.WithResearch(t.Context(), func(context.Context) error {
			close(secondStarted)
			return nil
		})
	}()
	<-secondAttempted
	select {
	case <-secondStarted:
		t.Fatal("second research started before first session close")
	default:
	}
	close(closeAllowed)
	require.NoError(t, <-firstDone)
	<-secondStarted
	require.NoError(t, <-secondDone)
}

func maximumConcurrent(t *testing.T, scope *r42concurrency.Scope, count int) int {
	t.Helper()
	release := make(chan struct{})
	var active atomic.Int64
	var maximum atomic.Int64
	var arrivals atomic.Int64
	var started sync.WaitGroup
	started.Add(scope.Limit())
	errs := make(chan error, count)
	var workers sync.WaitGroup
	workers.Add(count)
	for range count {
		go func() {
			defer workers.Done()
			errs <- scope.WithResearch(t.Context(), func(context.Context) error {
				current := active.Add(1)
				for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
				}
				if arrivals.Add(1) <= int64(scope.Limit()) {
					started.Done()
				}
				<-release
				active.Add(-1)
				return nil
			})
		}()
	}
	started.Wait()
	close(release)
	workers.Wait()
	for range count {
		require.NoError(t, <-errs)
	}
	return int(maximum.Load())
}
