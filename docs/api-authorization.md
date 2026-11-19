# User application permission context

App mounts resolve application:<user_id>:<app> through the authority application-context endpoint,
then consume matching snapshot/entries. Missing endpoints, unknown applications and malformed
bindings fail closed. No Agent or user policy fallback is supported. The authority gates
inactive grants and publishes application/path generations for live mounted revocation.
The one-time authority migration retains former Agent ACLs under application subjects.
The FUSE core, path spelling and runtime capability narrowing remain unchanged.
