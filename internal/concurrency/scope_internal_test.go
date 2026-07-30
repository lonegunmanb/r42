package concurrency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancellationAfterAncestorAcquisitionReleasesAncestor(t *testing.T) {
	t.Parallel()

	root, err := NewScope(2)
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
	defer cancel()
	secondAttempted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondAttempted)
		secondDone <- module.WithResearch(ctx, func(context.Context) error { return nil })
	}()
	<-secondAttempted
	require.Eventually(t, func() bool {
		return len(root.semaphores[0]) == cap(root.semaphores[0])
	}, time.Second, time.Millisecond)

	cancel()
	require.ErrorIs(t, <-secondDone, context.Canceled)
	rootOnlyStarted := make(chan struct{})
	rootOnlyDone := make(chan error, 1)
	go func() {
		rootOnlyDone <- root.WithResearch(t.Context(), func(context.Context) error {
			close(rootOnlyStarted)
			return nil
		})
	}()
	select {
	case <-rootOnlyStarted:
	case <-time.After(time.Second):
		t.Fatal("root permit was not released after canceled acquisition")
	}
	require.NoError(t, <-rootOnlyDone)
	close(releaseFirst)
	require.NoError(t, <-firstDone)
	assert.Empty(t, root.semaphores[0])
}
