package toolfs

import (
	"context"
	"time"
)

// WatchStorage refreshes entries independently of permission events.
// The provider supplies inventory metadata; no host storage root is scanned.
// It exits when the daemon context ends. Call in a managed goroutine.
func (s *MountSet) WatchStorage(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reproject(ctx)
		}
	}
}
