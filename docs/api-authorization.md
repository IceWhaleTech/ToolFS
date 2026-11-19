# User application permission context

App mounts resolve their permission subject through the authority's
`/v2/permissions/application-context`, then consume matching snapshot/entries.
The authority gates inactive API grants and publishes application/agent/path
generation changes; mounted decisions remain live. No user policy fallback is
permitted. Only old authorities with an unknown context route may use the
previous narrow agent subject. Explicit unknown-app responses fail closed.
The FUSE core, path spelling and capability narrowing remain unchanged.
