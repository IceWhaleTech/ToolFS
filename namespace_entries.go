package toolfs

import (
	"path"
	"sort"
	"strings"
)

// entryGrants intersects navigation metadata with effective snapshot roots.
// In particular, a broad /media rule is bounded by the returned inventories,
// while a shared descendant keeps all of its public ancestry in the view.
func entryGrants(access *AccessSnapshot, caps AgentCapabilities) []*NamespaceEntry {
	var pending []*NamespaceEntry
	seen := map[string]bool{}
	home := access.canonicalPath("/media/Home")
	for _, metadata := range access.entries {
		root := access.canonicalPath(metadata.Path)
		for _, grant := range access.Grants {
			host := access.canonicalPath(grant.Path)
			if host == "*" || isPathUnder(root, host) {
				host = root
			} else if !isPathUnder(host, root) {
				continue
			}
			if metadata.Kind == "home" && host == root {
				continue
			} // never open the Home collection
			if metadata.Kind == "storage" && isPathUnder(host, home) {
				continue
			}
			if !access.Allows(host, OpRead, caps) {
				continue
			}
			rest := strings.TrimPrefix(host, root)
			vp := path.Clean(metadata.ViewPath + rest)
			if seen[vp] {
				continue
			}
			seen[vp] = true
			pending = append(pending, &NamespaceEntry{GrantID: grant.ID, VirtualName: path.Base(vp), ViewPath: vp, HostPath: host, ReadOnly: !access.Allows(host, OpModify, caps)})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ViewPath < pending[j].ViewPath })
	return pending
}

// backendPath uses only equivalences declared by the authority. Never resolve
// arbitrary data symlinks: OpenRoot rejects every non-declared component.
func (s *AccessSnapshot) backendPath(host string) string {
	canonical := s.canonicalPath(host)
	bestCanonical, bestAlias := "", ""
	for _, alias := range s.pathAliases {
		prefix := s.canonicalPath(alias.Canonical)
		if alias.Alias == "/media/Home" || strings.HasPrefix(alias.Alias, "/media/") || alias.Alias == prefix {
			continue
		}
		if isPathUnder(canonical, prefix) && len(prefix) > len(bestCanonical) {
			bestCanonical, bestAlias = prefix, alias.Alias
		}
	}
	if bestAlias != "" {
		return path.Clean(bestAlias + strings.TrimPrefix(canonical, bestCanonical))
	}
	return canonical
}

func (v *InstanceView) nodeAt(vp string) *viewNode {
	v.mu.RLock()
	defer v.mu.RUnlock()
	n := v.root
	for _, segment := range viewSegments(vp) {
		if n == nil {
			return nil
		}
		n = n.children[segment]
	}
	return n
}

func (v *InstanceView) nodeVisible(n *viewNode) bool {
	if n == nil {
		return false
	}
	if n.entry != nil {
		rel := ""
		if n.entry.fileName != "" {
			rel = n.entry.fileName
		}
		if v.Allows(n.entry, rel, OpRead) {
			return true
		}
	}
	for _, child := range n.children {
		if v.nodeVisible(child) {
			return true
		}
	}
	return false
}
