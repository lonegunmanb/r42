package progress

import (
	"fmt"
)

// Frame is one handshake or worker-to-r42 frame. hello and ready are written
// by r42 to stdout; select is the only worker-to-r42 frame and is written by
// the worker to r42's stdin.
type Frame interface {
	// Type is the wire frame type: "hello", "select", or "ready".
	Type() string
	// Validate rejects malformed or unsupported frames before encoding.
	Validate() error
}

// HelloFrame is the first line r42 writes and flushes on stdout.
type HelloFrame struct {
	HandshakeVersion        int    `json:"handshake_version"`
	Protocol                string `json:"protocol"`
	SupportedSchemaVersions []int  `json:"supported_schema_versions"`
	R42Version              string `json:"r42_version"`
}

// SelectFrame is written by the worker to r42's stdin, choosing the highest
// schema major it supports from the advertised set.
type SelectFrame struct {
	HandshakeVersion int `json:"handshake_version"`
	SchemaVersion    int `json:"schema_version"`
}

// ReadyFrame confirms the negotiated schema major; r42 writes it and flushes
// it before planning or applying.
type ReadyFrame struct {
	HandshakeVersion int `json:"handshake_version"`
	SchemaVersion    int `json:"schema_version"`
}

func NewHelloFrame() HelloFrame {
	return HelloFrame{
		HandshakeVersion:        HandshakeVersion,
		Protocol:                Protocol,
		SupportedSchemaVersions: AdvertisedSchemaMajors(),
		R42Version:              R42Version,
	}
}

func NewSelectFrame(major int) SelectFrame {
	return SelectFrame{HandshakeVersion: HandshakeVersion, SchemaVersion: major}
}

func NewReadyFrame(major int) ReadyFrame {
	return ReadyFrame{HandshakeVersion: HandshakeVersion, SchemaVersion: major}
}

func (h HelloFrame) Type() string { return "hello" }

func (h HelloFrame) Validate() error {
	if h.HandshakeVersion != HandshakeVersion {
		return fmt.Errorf("unsupported handshake_version %d: want %d", h.HandshakeVersion, HandshakeVersion)
	}
	if h.Protocol != Protocol {
		return fmt.Errorf("unsupported protocol %q: want %q", h.Protocol, Protocol)
	}
	if len(h.SupportedSchemaVersions) == 0 {
		return fmt.Errorf("supported_schema_versions must not be empty")
	}
	if !validSafeName(h.R42Version) {
		return fmt.Errorf("r42_version is required")
	}
	for _, major := range h.SupportedSchemaVersions {
		if !supportedSchemaMajor(major) {
			return fmt.Errorf("unsupported schema major %d", major)
		}
	}
	return nil
}

func (s SelectFrame) Type() string { return "select" }

func (s SelectFrame) Validate() error {
	if s.HandshakeVersion != HandshakeVersion {
		return fmt.Errorf("unsupported handshake_version %d: want %d", s.HandshakeVersion, HandshakeVersion)
	}
	if !supportedSchemaMajor(s.SchemaVersion) {
		return fmt.Errorf("unsupported schema_version %d", s.SchemaVersion)
	}
	return nil
}

func (r ReadyFrame) Type() string { return "ready" }

func (r ReadyFrame) Validate() error {
	if r.HandshakeVersion != HandshakeVersion {
		return fmt.Errorf("unsupported handshake_version %d: want %d", r.HandshakeVersion, HandshakeVersion)
	}
	if !supportedSchemaMajor(r.SchemaVersion) {
		return fmt.Errorf("unsupported schema_version %d", r.SchemaVersion)
	}
	return nil
}
