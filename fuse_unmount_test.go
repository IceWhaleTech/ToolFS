//go:build linux || darwin

package toolfs

import (
	"errors"
	"testing"
)

type retryMountServer struct {
	err   error
	calls int
}

func (s *retryMountServer) Unmount() error { s.calls++; return s.err }
func (s *retryMountServer) Wait()          {}

type countedCloseRoot struct {
	RootHandle
	calls int
}

func (r *countedCloseRoot) Close() error { r.calls++; return nil }

func TestMountHandleRetainsRootsUntilUnmountSucceeds(t *testing.T) {
	server := &retryMountServer{err: errors.New("busy")}
	root := &countedCloseRoot{}
	handle := &mountHandle{server: server, view: &InstanceView{grants: []*NamespaceEntry{{root: root}}}}
	if err := handle.Unmount(); err == nil {
		t.Fatal("failed unmount reported success")
	}
	if handle.unmounted || root.calls != 0 {
		t.Fatal("failed unmount discarded the live view's roots")
	}
	server.err = nil
	if err := handle.Unmount(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Unmount(); err != nil {
		t.Fatal(err)
	}
	if !handle.unmounted || server.calls != 2 || root.calls != 1 {
		t.Fatalf("unmount was not retried exactly once: server=%d roots=%d", server.calls, root.calls)
	}
}
