package ui

import "fmt"

type Mode string

const (
	ModeAuto  Mode = "auto"
	ModeTUI   Mode = "tui"
	ModeREPL  Mode = "repl"
	ModeJSONL Mode = "jsonl"
)

const (
	minimumWidth  = 50
	minimumHeight = 12
)

type Terminal struct {
	Input  bool
	Output bool
	Width  int
	Height int
	TERM   string
	CI     bool
}

// Valid reports whether requested is one of the accepted UI mode values.
func Valid(requested Mode) bool {
	switch requested {
	case ModeAuto, ModeTUI, ModeREPL, ModeJSONL:
		return true
	default:
		return false
	}
}

func ResolveMode(requested Mode, terminal Terminal) (Mode, error) {
	supported := terminal.Input && terminal.Output && terminal.TERM != "dumb" && !terminal.CI &&
		terminal.Width >= minimumWidth && terminal.Height >= minimumHeight
	switch requested {
	case ModeAuto:
		if supported {
			return ModeTUI, nil
		}
		return ModeREPL, nil
	case ModeREPL:
		return ModeREPL, nil
	case ModeTUI:
		if !supported {
			return "", fmt.Errorf("tui requires an interactive terminal of at least %dx%d", minimumWidth, minimumHeight)
		}
		return ModeTUI, nil
	case ModeJSONL:
		return ModeJSONL, nil
	default:
		return "", fmt.Errorf("invalid ui mode %q: want auto, tui, repl, or jsonl", requested)
	}
}
