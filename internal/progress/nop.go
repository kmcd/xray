package progress

// NopSink is the zero-cost default. Used when --output quiet is in
// effect and when run.Options.Progress is nil.
type NopSink struct{}

func (NopSink) Emit(Event) {}
