// Command toolfsd serves per-user ToolFS mounts on the host.
//
// It resolves an agent account to a ZimaOS authorization subject, pulls that
// user's grants, and projects them into a FUSE mount that an orchestrator can
// bind into the user's instance container. The mount point carries the
// identity: whichever instance receives it is confined to that user's grants,
// while the instance itself stays identity-agnostic.
//
// Typical invocation:
//
//	zimaos-toolfsd -root /run/toolfs/mounts -socket /run/toolfs/toolfsd.sock
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/IceWhaleTech/toolfs"
)

func main() {
	var (
		root         = flag.String("root", "/run/toolfs/mounts", "directory under which per-user mount points are created")
		socketPath   = flag.String("socket", "/run/toolfs/toolfsd.sock", "unix socket for the control API")
		bindingsPath = flag.String("bindings", "", "JSON file mapping agent accounts to ZimaOS users")
		runtimePath  = flag.String("runtime-path", "/var/run/casaos", "directory where ZimaOS services publish their addresses")
		cachePath    = flag.String("permission-cache", "/var/lib/casaos/toolfs/permission-cache.json", "where authorization snapshots persist across restarts (empty disables)")
		allowDelete  = flag.Bool("allow-delete", false, "grant delete on read-write grants (withheld by default)")
		fallbackUser = flag.String("fallback-user", "", "ZimaOS username used for unbound agent accounts (transition mode)")
		fallbackUID  = flag.Int("fallback-uid", 0, "ZimaOS numeric uid paired with -fallback-user")
		debug        = flag.Bool("debug", false, "enable FUSE protocol logging")
	)
	flag.Parse()

	if err := run(*root, *socketPath, *bindingsPath, *runtimePath, *cachePath,
		*allowDelete, *fallbackUser, *fallbackUID, *debug); err != nil {
		log.Fatalf("toolfsd: %v", err)
	}
}

func run(root, socketPath, bindingsPath, runtimePath, cachePath string,
	allowDelete bool, fallbackUser string, fallbackUID int, debug bool) error {

	if !toolfs.FUSEAvailable() {
		return fmt.Errorf("no usable FUSE implementation (Linux needs /dev/fuse)")
	}

	resolver, err := buildResolver(bindingsPath, fallbackUser, fallbackUID)
	if err != nil {
		return err
	}

	// The watch outlives everything below it; the shutdown signal is what ends
	// it, so the same context serves both.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provider := toolfs.StartAuthorityPermissionProvider(ctx, runtimePath, cachePath)

	mounts, err := toolfs.NewMountSet(toolfs.MountSetConfig{
		Root:         root,
		DefaultCaps:  toolfs.AgentCapabilities{AllowDelete: allowDelete},
		MountOptions: toolfs.MountConfig{Debug: debug},
	}, resolver, provider)
	if err != nil {
		return err
	}

	// A change to the authority — any grant added or revoked anywhere, including
	// a share to a user that a different account edited — reprojects every live
	// mount, so a running instance's view tracks the current grants without a
	// remount. Only mounts whose grant set actually changed are rebuilt.
	provider.OnChange(func(int64) { mounts.Reproject(ctx) })

	control := toolfs.NewControlServer(mounts, socketPath)
	if err := control.Listen(); err != nil {
		return fmt.Errorf("cannot listen on %s: %w", socketPath, err)
	}

	log.Printf("toolfsd: serving control API on %s, mount root %s", socketPath, root)
	if !allowDelete {
		log.Printf("toolfsd: delete is withheld by default; read/write access follows current authorization")
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- control.Serve() }()

	// Tear every mount down on shutdown. Leaving them behind would strand FUSE
	// mounts whose server is gone, which consumers see as ENOTCONN.
	select {
	case err := <-serveErr:
		_ = control.Close()
		_ = mounts.Close()
		return err
	case <-ctx.Done():
		log.Printf("toolfsd: shutting down, unmounting %d mount(s)", len(mounts.List()))
		_ = control.Close()
		if err := mounts.Close(); err != nil {
			return fmt.Errorf("unmount errors during shutdown: %w", err)
		}
		return nil
	}
}

// buildResolver wires optional legacy identity bindings.
//
// A trusted control client can supply the identity from its verified ZimaOS
// token. Static bindings are only needed for requests without that identity.
func buildResolver(bindingsPath, fallbackUser string, fallbackUID int) (toolfs.IdentityResolver, error) {
	bindings := make(map[string]toolfs.ZimaIdentity)

	if bindingsPath != "" {
		data, err := os.ReadFile(bindingsPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read bindings: %w", err)
		}
		if err := json.Unmarshal(data, &bindings); err != nil {
			return nil, fmt.Errorf("malformed bindings file: %w", err)
		}
		log.Printf("toolfsd: loaded %d account binding(s) from %s", len(bindings), bindingsPath)
	}

	resolver := toolfs.NewStaticIdentityResolver(bindings)

	if fallbackUser != "" {
		// Transition mode: every unbound agent account maps to one dedicated
		// ZimaOS user. That yields full accidental-deletion protection with no
		// isolation between users yet, so it is opt-in and loudly logged.
		resolver.SetFallback(&toolfs.ZimaIdentity{Username: fallbackUser, UserID: fallbackUID})
		log.Printf("toolfsd: WARNING transition mode active — every unbound agent "+
			"account resolves to ZimaOS user %q, so users are NOT isolated from "+
			"each other", fallbackUser)
	} else if len(bindings) == 0 {
		log.Printf("toolfsd: no legacy bindings configured; mount requests must supply a ZimaOS identity")
	}

	return resolver, nil
}
