package progress

import "context"

type encoderContextKey struct{}

// WithEncoder returns a context carrying the negotiated progress encoder.
// The CLI threads the encoder through the context so the run and future
// publisher stages can emit records without a separate wiring path.
func WithEncoder(ctx context.Context, encoder *Encoder) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, encoderContextKey{}, encoder)
}

// EncoderFromContext returns the progress encoder stored by WithEncoder, or
// nil when none is present.
func EncoderFromContext(ctx context.Context) *Encoder {
	if ctx == nil {
		return nil
	}
	encoder, _ := ctx.Value(encoderContextKey{}).(*Encoder)
	return encoder
}
