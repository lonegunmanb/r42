package progress

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

// Encoder writes line-oriented JSONL protocol frames and versioned event
// records to one writer. It owns the negotiation handshake (hello/select/
// ready) and the schema-major-specific record encoding. Frames and records
// written through the same Encoder share the same protocol and schema major.
type Encoder struct {
	writer       io.Writer
	major        int
	encodeRecord schemaRecordEncoder
}

type schemaRecordEncoder func(Record) (any, bool)

// NewEncoder returns the encoder for the given schema major. Every frame and
// record written through it is encoded for that major.
func NewEncoder(writer io.Writer, major int) (*Encoder, error) {
	if nilWriter(writer) {
		return nil, fmt.Errorf("progress writer is required")
	}
	definition, err := schemaDefinitionFor(major)
	if err != nil {
		return nil, err
	}
	return &Encoder{writer: writer, major: major, encodeRecord: definition.encode}, nil
}

func nilWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// EncodeFrame validates and writes one handshake frame followed by a newline.
// Value and pointer frames are both accepted; pointer frames are dereferenced.
func (e *Encoder) EncodeFrame(frame Frame) error {
	frame = derefFrame(frame)
	var wire any
	switch typed := frame.(type) {
	case HelloFrame:
		if err := typed.Validate(); err != nil {
			return err
		}
		wire = struct {
			Type                    string `json:"type"`
			HandshakeVersion        int    `json:"handshake_version"`
			Protocol                string `json:"protocol"`
			SupportedSchemaVersions []int  `json:"supported_schema_versions"`
			R42Version              string `json:"r42_version"`
		}{
			Type: frame.Type(), HandshakeVersion: typed.HandshakeVersion, Protocol: typed.Protocol,
			SupportedSchemaVersions: typed.SupportedSchemaVersions, R42Version: typed.R42Version,
		}
	case SelectFrame:
		if err := typed.Validate(); err != nil {
			return err
		}
		wire = struct {
			Type             string `json:"type"`
			HandshakeVersion int    `json:"handshake_version"`
			SchemaVersion    int    `json:"schema_version"`
		}{Type: frame.Type(), HandshakeVersion: typed.HandshakeVersion, SchemaVersion: typed.SchemaVersion}
	case ReadyFrame:
		if err := typed.Validate(); err != nil {
			return err
		}
		wire = struct {
			Type             string `json:"type"`
			HandshakeVersion int    `json:"handshake_version"`
			SchemaVersion    int    `json:"schema_version"`
		}{Type: frame.Type(), HandshakeVersion: typed.HandshakeVersion, SchemaVersion: typed.SchemaVersion}
	default:
		return fmt.Errorf("unknown frame type %T", frame)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("encode %s frame: %w", frame.Type(), err)
	}
	encoded = append(encoded, '\n')
	if err = writeLine(e.writer, encoded); err != nil {
		return fmt.Errorf("write %s frame: %w", frame.Type(), err)
	}
	return nil
}

// derefFrame converts a pointer frame to its value so the encoder switch only
// needs the three value cases.
func derefFrame(frame Frame) Frame {
	switch typed := frame.(type) {
	case *HelloFrame:
		if typed != nil {
			return *typed
		}
		return nil
	case *SelectFrame:
		if typed != nil {
			return *typed
		}
		return nil
	case *ReadyFrame:
		if typed != nil {
			return *typed
		}
		return nil
	}
	return frame
}

type wireEnvelope struct {
	Type          string    `json:"type"`
	Critical      bool      `json:"critical"`
	Protocol      string    `json:"protocol"`
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id,omitempty"`
	Sequence      uint64    `json:"sequence"`
	Timestamp     time.Time `json:"timestamp,omitzero"`
}

// EncodeRecord validates one allowlisted record, wraps it in the common
// envelope for the negotiated schema major, and writes one JSONL line.
func (e *Encoder) EncodeRecord(envelope Envelope, record Record) error {
	if err := validateEnvelope(envelope); err != nil {
		return err
	}
	wire, ok := e.encodeRecord(record)
	if !ok {
		return fmt.Errorf("unknown record type %T for schema major %d", record, e.major)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	timestamp := envelope.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	} else {
		timestamp = timestamp.UTC()
	}
	envelopeObject := wireEnvelope{
		Type: record.Type(), Critical: record.Critical(), Protocol: Protocol,
		SchemaVersion: e.major, RunID: envelope.RunID, Sequence: envelope.Sequence,
		Timestamp: timestamp,
	}
	frame, err := mergeJSON(envelopeObject, wire)
	if err != nil {
		return fmt.Errorf("encode %s record: %w", record.Type(), err)
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode %s record: %w", record.Type(), err)
	}
	encoded = append(encoded, '\n')
	if err = writeLine(e.writer, encoded); err != nil {
		return fmt.Errorf("write %s record: %w", record.Type(), err)
	}
	return nil
}

// schema1WireRecord returns the deterministic per-type wire payload for a
// schema-1 record. The map-key ordering of the final JSON line is still not
// stable, so consumers must never parse records by byte comparison; frames
// (handshake lines) use fixed structs and are deterministic.
func schema1WireRecord(record Record) (any, bool) {
	switch typed := record.(type) {
	case *RunSnapshotRecord:
		return typed, typed != nil
	case *DynamicTasksMaterializedRecord:
		return typed, typed != nil
	case *NodeRecord:
		return typed, typed != nil
	case *TimelineRecord:
		return typed, typed != nil
	case *RunCompletedRecord:
		return typed, typed != nil
	case *RunFailedRecord:
		return typed, typed != nil
	case *RunCanceledRecord:
		return typed, typed != nil
	default:
		return nil, false
	}
}

// mergeJSON marshals both values and overlays the envelope fields onto the
// record fields, so the envelope's type/critical fields win for collisions.
func mergeJSON(envelope, record any) (map[string]any, error) {
	frame := make(map[string]any)
	if err := decodeInto(frame, record); err != nil {
		return nil, err
	}
	if err := decodeInto(frame, envelope); err != nil {
		return nil, err
	}
	return frame, nil
}

func decodeInto(target map[string]any, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, &target)
}

func writeLine(writer io.Writer, line []byte) error {
	written, err := writer.Write(line)
	if err != nil {
		return err
	}
	if written != len(line) {
		return io.ErrShortWrite
	}
	return nil
}
