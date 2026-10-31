package msgstore

import (
	"context"

	"github.com/biodream-llc/perfuse/internal/chanscope"
)

// channelScope is chanscope.SQL, named for the queries here.
func channelScope(ctx context.Context, column string) (string, []any) {
	return chanscope.SQL(ctx, column)
}
