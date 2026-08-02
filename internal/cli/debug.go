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
		activeRun, err := run.NewManager(directory).Reserve()
		if err != nil {
			return ctx, nil, nil, err
		}
		return s.ensureRunLocked(ctx, activeRun)
	}
	return debuglog.WithRecorder(ctx, s.recorder), s.run, s.recorder, nil
}

func (s *debugRun) ensureRun(
	ctx context.Context,
	activeRun *run.Run,
) (context.Context, *run.Run, *debuglog.Recorder, error) {
	if s == nil || !s.enabled {
		return ctx, activeRun, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureRunLocked(ctx, activeRun)
}

func (s *debugRun) ensureRunLocked(
	ctx context.Context,
	activeRun *run.Run,
) (context.Context, *run.Run, *debuglog.Recorder, error) {
	if activeRun == nil {
		return ctx, nil, nil, fmt.Errorf("debug run is required")
	}
	if s.run != nil && filepath.Clean(s.run.Directory()) != filepath.Clean(activeRun.Directory()) {
		return ctx, nil, nil, fmt.Errorf(
			"debug run %q does not match planned run %q", s.run.Directory(), activeRun.Directory(),
		)
	}
	if s.recorder == nil {
		if err := activeRun.Ensure(); err != nil {
			return ctx, nil, nil, err
		}
		recorder, err := debuglog.NewRecorder(activeRun.Directory(), true)
		if err != nil {
			return ctx, nil, nil, err
		}
		s.run = activeRun
		s.recorder = recorder
	}
	s.recorder.SetEventBus(debuglog.EventBusFromContext(ctx))
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
