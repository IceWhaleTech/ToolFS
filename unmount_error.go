//go:build linux || darwin

package toolfs

import (
	"errors"
	"os"
	"syscall"
)

type unmountError struct {
	stage string
	err   error
}

func (e *unmountError) Error() string { return e.err.Error() }
func (e *unmountError) Unwrap() error { return e.err }

type mountLayoutError string

func (e mountLayoutError) Error() string { return string(e) }

// unmountDiagnostics classifies causes, not text, for consumers that must not
// publish the root-only control API's raw error message or filesystem paths.
func unmountDiagnostics(err error) (stage, code string) {
	stage, code = "unknown", "unknown"
	var failure *unmountError
	if errors.As(err, &failure) {
		switch failure.stage {
		case "fuse-unmount", "directory-cleanup":
			stage = failure.stage
		}
	}
	var layout mountLayoutError
	switch {
	case errors.As(err, &layout):
		code = "invalid-layout"
	case errors.Is(err, syscall.EBUSY):
		code = "busy"
	case errors.Is(err, os.ErrPermission):
		code = "permission-denied"
	case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST):
		code = "not-empty"
	}
	return stage, code
}
