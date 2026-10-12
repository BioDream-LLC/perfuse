package msgstore

import (
	"context"

	"github.com/biodream-llc/perfuse/internal/chanscope"
)

// WithChannels limits reads on ctx to these channels; see package chanscope.
func WithChannels(ctx context.Context, channels []string) context.Context {
	return chanscope.With(ctx, channels)
}

// channelScope is chanscope.SQL, named for the queries here.
func channelScope(ctx context.Context, column string) (string, []any) {
	return chanscope.SQL(ctx, column)
}
