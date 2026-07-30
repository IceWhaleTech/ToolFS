package toolfs

// Path security primitives.
//
// The division of labour matters:
//   - normalizeVirtualPath performs *lexical* normalization only — backslash to
//     slash, collapsing repeated slashes, dropping a "./" prefix and a trailing
//     slash. It does not clean "..", and several existing call sites depend on
//     that, so it is left alone.
//   - sanitizeVirtualPath adds *semantic* validation on top, rejecting anything
//     that could change which mount a path belongs to.
//
// Mixing the two is exactly what allowed path traversal: the authorization check
// went through normalizeVirtualPath (which preserves ".."), while execution went
// through filepath.Join (which cleans it), so one input yielded two different
// paths. Rejecting ".." at the entry point makes that divergence structurally
// impossible rather than patched over.

import (
	"errors"
	"strings"
)

var (
	// ErrInvalidPath reports an empty path or one containing illegal bytes.
	ErrInvalidPath = errors.New("toolfs: invalid path")
	// ErrPathTraversal reports a ".." segment that could escape the mount.
	ErrPathTraversal = errors.New("toolfs: path traversal rejected")
	// ErrPathEscapesRoot reports a resolved path landing outside its mount.
	ErrPathEscapesRoot = errors.New("toolfs: path escapes mount root")
	// ErrReadOnlyMount reports a write attempt against a read-only mount.
	ErrReadOnlyMount = errors.New("toolfs: read-only mount")
)

// sanitizeVirtualPath normalizes and validates a virtual path.
//
// It rejects the empty path, paths containing a NUL byte, and any ".." segment.
// Callers must use only the returned value from then on, so that the
// authorization decision and the execution act on the same path.
func sanitizeVirtualPath(path string) (string, error) {
	if path == "" {
		return "", ErrInvalidPath
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", ErrInvalidPath
	}

	normalized := normalizeVirtualPath(path)
	if normalized == "" {
		return "", ErrInvalidPath
	}

	// Check per segment. strings.Contains(path, "..") would wrongly reject
	// legitimate names such as "..foo".
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}

	return normalized, nil
}

// pathHasPrefix reports whether path sits under prefix, comparing by path
// segment rather than by raw string prefix.
//
// The raw form let "/toolfs/database" match the mount point "/toolfs/data".
func pathHasPrefix(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	if path == prefix {
		return true
	}

	// Fold the trailing slash so "/a/" and "/a" are the same prefix.
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		// prefix is the root "/", which contains every absolute path.
		return strings.HasPrefix(path, "/")
	}
	if path == trimmed {
		return true
	}
	if !strings.HasPrefix(path, trimmed) {
		return false
	}

	// The boundary must be a separator. Besides "/", "?" also counts: the
	// virtual backends encode a query string directly in the path, as in
	// "/toolfs/rag?text=x", and readRAG splits on "?" for exactly that reason.
	//
	// Accepting "?" introduces no escape risk. String matching only selects
	// *which mount* applies; the real boundary is enforced by RootHandle
	// (os.Root, backed by openat2 RESOLVE_BENEATH on Linux), so even a real
	// filename containing "?" stays confined to its mount.
	switch path[len(trimmed)] {
	case '/', '?':
		return true
	default:
		return false
	}
}

// relativeUnder returns the portion of path below prefix, without a leading
// slash, plus whether path is under prefix at all. An exact match yields "".
func relativeUnder(path, prefix string) (string, bool) {
	if !pathHasPrefix(path, prefix) {
		return "", false
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if path == trimmed || path == prefix {
		return "", true
	}
	rel := strings.TrimPrefix(path, trimmed)
	// Strip only "/". A "?" belongs to the relative part, since the virtual
	// backends need it to parse their query string.
	return strings.TrimPrefix(rel, "/"), true
}
