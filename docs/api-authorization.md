# User application permission context

App mounts resolve application:<user_id>:<app> through the authority application-context endpoint,
then consume matching snapshot/entries. Missing endpoints, unknown applications and malformed
bindings fail closed. No Agent or user policy fallback is supported. The authority gates
inactive grants and publishes application/path generations for live mounted revocation.
The one-time authority migration retains former Agent ACLs under application subjects.
The FUSE core, path spelling and runtime capability narrowing remain unchanged.

Common is pinned to github.com/IceWhaleTech/ZimaOS-Common/v2 v2.0.0-alpha1.
All Common imports use /v2 so identity and permission contexts share one module version.
The HTTP authorization contract and data migration behavior are unchanged.
