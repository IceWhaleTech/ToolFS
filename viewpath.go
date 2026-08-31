package toolfs

// Host-path → view-path aliasing.
//
// The mount view is not the raw host filesystem. A user of ZimaOS sees storage
// *spaces* (the system disk "ZimaOS-HD", each attached disk, and a private
// "Home"), never the internal spellings the authority grants in (/DATA,
// /DATA/.home, /media/<disk>). The projection therefore rewrites each grant's
// host path through an alias table before it is placed in the namespace tree.
//
// One table is the single source of truth: toolfsd applies it forward
// (host → view) to build the mount, and the agent-side dispatcher applies the
// same table to translate a host path into the path the agent works under. Keep
// them the same table or the two views drift.
//
// The table is data, not code: nothing here knows what "topics" or an "agent
// home" is. It only knows prefixes. ZimaOS supplies the entries; the default
// below encodes today's conventions and can be overridden per mount.

import (
	"path"
	"strings"
)

// PathAlias rewrites a host-path prefix to a view-path prefix. Matching is by
// path component (a prefix matches only at a "/" boundary, never mid-segment),
// and the longest matching Host wins, so a more specific alias overrides a
// broader one.
type PathAlias struct {
	// Host is the host-path prefix, e.g. "/DATA/.home".
	Host string
	// View is what it becomes in the mount view, e.g. "/Home". "/" means the
	// prefix is stripped and the next host segment becomes a top-level view
	// entry (how "/media" exposes each disk as its own space).
	View string
}

// DefaultZimaViewAliases encodes ZimaOS's storage conventions:
//
//   - Homes live under /DATA/.home (also reachable as /media/.home and the
//     FHS /home); all three spellings collapse to the private "/Home" space.
//   - /DATA and /media/ZimaOS-HD are the same filesystem — the system disk,
//     which the user knows as "ZimaOS-HD".
//   - Every other disk sits at /media/<label> and is its own space, so /media
//     is stripped and <label> becomes a top-level view entry.
//
// Home aliases are listed first only for readability; selection is by longest
// Host, so "/DATA/.home" always beats "/DATA" regardless of order.
var DefaultZimaViewAliases = []PathAlias{
	// The homes live on the system disk, reachable by every spelling of it —
	// /DATA, /media/ZimaOS-HD — plus the /media and FHS entrances. All collapse
	// to /Home so the home namespace is one place no matter which spelling a
	// grant used, and so .home never surfaces beneath the disk's own space.
	{Host: "/DATA/.home", View: "/Home"},
	{Host: "/media/.home", View: "/Home"},
	{Host: "/media/ZimaOS-HD/.home", View: "/Home"},
	{Host: "/home", View: "/Home"},
	{Host: "/DATA", View: "/ZimaOS-HD"},
	{Host: "/media/ZimaOS-HD", View: "/ZimaOS-HD"},
	{Host: "/media", View: "/"},
}

// viewPath rewrites a host path into its view path under the alias table.
//
// The longest matching Host prefix wins (component-aligned). A host path that
// matches no alias is mirrored unchanged: an explicit grant on some path the
// table does not mention still appears, at its real location, rather than
// vanishing. The result is always cleaned and absolute.
func viewPath(hostPath string, aliases []PathAlias) string {
	hp := path.Clean("/" + strings.TrimSpace(hostPath))

	bestHost := ""
	bestView := ""
	found := false
	for _, alias := range aliases {
		h := path.Clean("/" + alias.Host)
		if hp != h && !strings.HasPrefix(hp, h+"/") {
			continue
		}
		if !found || len(h) > len(bestHost) {
			bestHost = h
			bestView = alias.View
			found = true
		}
	}
	if !found {
		return hp
	}

	rest := strings.TrimPrefix(hp, bestHost)
	// rest is either "" (an exact match) or begins with "/". path.Clean folds a
	// "/" View (strip) and the joined rest into the right shape.
	return path.Clean(bestView + rest)
}

// viewSegments splits a view path into its components, dropping empties. The
// namespace tree is built and walked in terms of these.
func viewSegments(viewPath string) []string {
	trimmed := strings.Trim(viewPath, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
