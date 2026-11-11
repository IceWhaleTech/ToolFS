//go:build !linux && !darwin

package toolfs

import (
	"errors"
	"os"
)

func newSecureBackend() SecurePathBackend { return &unsupportedSecureBackend{} }

type unsupportedSecureBackend struct{}

func (*unsupportedSecureBackend) OpenRoot(path string) (RootHandle, error) {
	return nil, &os.PathError{Op: "openroot", Path: path, Err: errors.ErrUnsupported}
}
