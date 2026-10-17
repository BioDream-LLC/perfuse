package main

import (
	"context"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/terminology"
)

// isMemoryPath reports an in-memory database, beside which there is no directory to keep a second one in.
func isMemoryPath(path string) bool {
	return path == ":memory:" || strings.HasPrefix(path, "file::memory:") || strings.Contains(path, "mode=memory")
}

// forgetOldUnmapped drops unmapped values not seen within the retention window, once a day: they are about messages, and
// are kept no longer than the messages are.
func forgetOldUnmapped(ctx context.Context, s *terminology.Store, retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		_ = s.Forget(ctx, "", "", nil, time.Duration(retentionDays)*24*time.Hour)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
