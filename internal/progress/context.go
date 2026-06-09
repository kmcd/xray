package progress

import "context"

type sinkKey struct{}

// WithSink returns a context carrying sink as the ambient progress sink.
// Code paths that can't be threaded a Sink directly (notably the
// rate-limit transport, which is constructed per-connector before the
// run-wide sink exists) read the sink from the request context instead.
func WithSink(ctx context.Context, sink Sink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, sinkKey{}, sink)
}

// FromContext returns the ambient Sink, or NopSink when none is set.
// Callers can always Emit unconditionally.
func FromContext(ctx context.Context) Sink {
	if ctx == nil {
		return NopSink{}
	}
	if s, ok := ctx.Value(sinkKey{}).(Sink); ok && s != nil {
		return s
	}
	return NopSink{}
}
