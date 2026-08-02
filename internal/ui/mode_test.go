package ui_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requested ui.Mode
		terminal  ui.Terminal
		want      ui.Mode
		wantErr   string
	}{
		{
			name:      "auto selects tui for an interactive terminal",
			requested: ui.ModeAuto,
			terminal:  ui.Terminal{Input: true, Output: true, Width: 120, Height: 30},
			want:      ui.ModeTUI,
		},
		{
			name:      "auto falls back when output is redirected",
			requested: ui.ModeAuto,
			terminal:  ui.Terminal{Input: true, Width: 120, Height: 30},
			want:      ui.ModeREPL,
		},
		{
			name:      "auto falls back for dumb terminal",
			requested: ui.ModeAuto,
			terminal:  ui.Terminal{Input: true, Output: true, Width: 120, Height: 30, TERM: "dumb"},
			want:      ui.ModeREPL,
		},
		{
			name:      "auto falls back in ci",
			requested: ui.ModeAuto,
			terminal:  ui.Terminal{Input: true, Output: true, Width: 120, Height: 30, CI: true},
			want:      ui.ModeREPL,
		},
		{
			name:      "forced repl ignores terminal support",
			requested: ui.ModeREPL,
			terminal:  ui.Terminal{Input: true, Output: true, Width: 120, Height: 30},
			want:      ui.ModeREPL,
		},
		{
			name:      "forced tui reports unsupported terminal",
			requested: ui.ModeTUI,
			terminal:  ui.Terminal{},
			wantErr:   "tui requires an interactive terminal",
		},
		{
			name:      "unknown mode is rejected",
			requested: ui.Mode("graphical"),
			terminal:  ui.Terminal{Input: true, Output: true, Width: 120, Height: 30},
			wantErr:   "invalid ui mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := ui.ResolveMode(test.requested, test.terminal)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, actual)
		})
	}
}
