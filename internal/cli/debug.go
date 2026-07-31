package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/run"
)

type debugRunContextKey struct{}

type debugRun struct {
	mu       sync.Mutex
	enabled  bool
	run      *run.Run
	recorder *debuglog.Recorder
}

func withDebugRun(ctx context.Context, state *debugRun) context.Context {
	return context.WithValue(ctx, debugRunContextKey{}, state)
}

func debugRunFromContext(ctx context.Context) *debugRun {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(debugRunContextKey{}).(*debugRun)
	return state
}

func (s *debugRun) ensure(ctx context.Context, directory string) (context.Context, *run.Run, *debuglog.Recorder, error) {
	if s == nil || !s.enabled {
		return ctx, nil, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorder == nil {
		info, err := os.Stat(directory)
		if err != nil {
			return ctx, nil, nil, fmt.Errorf("inspect debug project directory: %w", err)
		}
		if !info.IsDir() {
			return ctx, nil, nil, fmt.Errorf("debug project path %q is not a directory", directory)
		}
		activeRun, err := run.NewManager(directory).Create()
		if err != nil {
			return ctx, nil, nil, err
		}
		recorder, err := debuglog.NewRecorder(activeRun.Directory(), true)
		if err != nil {
			return ctx, nil, nil, err
		}
		s.run = activeRun
		s.recorder = recorder
	}
	return debuglog.WithRecorder(ctx, s.recorder), s.run, s.recorder, nil
}

func (s *debugRun) close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorder == nil {
		return nil
	}
	err := s.recorder.Close()
	s.recorder = nil
	return err
}

func (s *debugRun) path() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return ""
	}
	return filepath.Join(s.run.Directory(), debuglog.EventsFileName)
}
