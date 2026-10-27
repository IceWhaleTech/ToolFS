package toolfs

import (
	"context"
	"fmt"
	"strings"

	"github.com/IceWhaleTech/ZimaOS-Common/permission"
)

// agentDeclared asks about statements, not effective permissions. A scoped
// agent with no owner or no readable paths must remain scoped and see nothing.
// The device-wide declaration set is rebuilt once per watched generation, never
// in the FUSE decision path.
func (p *AuthorityPermissionProvider) agentDeclared(ctx context.Context, subject string) (bool, int64, error) {
	p.scopeMu.Lock()
	defer p.scopeMu.Unlock()

	generation := p.client.Generation()
	if p.scopes != nil && p.scopeGeneration == generation {
		_, declared := p.scopes[subject]
		return declared, generation, nil
	}

	scopes := make(map[string]struct{})
	var tupleGeneration int64
	for i, query := range []struct{ namespace, relation string }{
		{permission.NamespaceAgent, permission.RelOwner},
		{permission.NamespacePath, permission.RelRead},
		{permission.NamespacePath, permission.RelWrite},
		{permission.NamespacePath, permission.RelDelete},
		{permission.NamespacePath, permission.RelSMB},
		{permission.NamespacePath, permission.RelOwner},
	} {
		list, err := p.client.Tuples(ctx, query.namespace, query.relation)
		if err != nil {
			return false, 0, fmt.Errorf("read agent scope declarations: %w", err)
		}
		if !list.Complete {
			return false, 0, fmt.Errorf("agent scope declarations are not complete")
		}
		if i == 0 {
			tupleGeneration = list.Generation
		} else if list.Generation != tupleGeneration {
			return false, 0, fmt.Errorf("agent scope declarations changed while being read")
		}
		for _, tuple := range list.Tuples {
			if query.namespace == permission.NamespaceAgent {
				scopes[permission.NamespaceAgent+":"+tuple.Object] = struct{}{}
			} else if strings.HasPrefix(tuple.Subject, permission.NamespaceAgent+":") {
				// Subject-set declarations also establish the concrete agent's
				// scope; they must not turn into a full user view if empty.
				concrete, _, _ := strings.Cut(tuple.Subject, "#")
				scopes[concrete] = struct{}{}
			}
		}
	}
	if p.client.Generation() != generation || tupleGeneration < generation {
		return false, 0, fmt.Errorf("agent scope generation changed while being read")
	}
	p.scopes, p.scopeGeneration = scopes, generation
	_, declared := scopes[subject]
	return declared, generation, nil
}
