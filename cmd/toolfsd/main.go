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
//	toolfsd -root /mnt/toolfs -socket /run/toolfs/toolfsd.sock -bindings /etc/toolfs/bindings.json
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
	"time"

	"github.com/IceWhaleTech/toolfs"
)

func main() {
	var (
		root         = flag.String("root", "/mnt/toolfs", "directory under which per-user mount points are created")
		socketPath   = flag.String("socket", "/run/toolfs/toolfsd.sock", "unix socket for the control API")
		bindingsPath = flag.String("bindings", "", "JSON file mapping agent accounts to ZimaOS users")
		filesURL     = flag.String("files-url", "", "IceWhale-Files base URL (default: read from the runtime file)")
		grantTTL     = flag.Duration("grant-ttl", toolfs.DefaultGrantTTL, "how long an authorization snapshot may be reused")
		allowDelete  = flag.Bool("allow-delete", false, "grant delete on read-write grants (withheld by default)")
		fallbackUser = flag.String("fallback-user", "", "ZimaOS username used for unbound agent accounts (transition mode)")
		fallbackUID  = flag.Int("fallback-uid", 0, "ZimaOS numeric uid paired with -fallback-user")
		debug        = flag.Bool("debug", false, "enable FUSE protocol logging")
	)
	flag.Parse()

	if err := run(*root, *socketPath, *bindingsPath, *filesURL, *grantTTL,
		*allowDelete, *fallbackUser, *fallbackUID, *debug); err != nil {
		log.Fatalf("toolfsd: %v", err)
	}
}

func run(root, socketPath, bindingsPath, filesURL string, grantTTL time.Duration,
	allowDelete bool, fallbackUser string, fallbackUID int, debug bool) error {

	if !toolfs.FUSEAvailable() {
		return fmt.Errorf("no usable FUSE implementation (Linux needs /dev/fuse)")
	}

	resolver, err := buildResolver(bindingsPath, fallbackUser, fallbackUID)
	if err != nil {
		return err
	}

	provider := toolfs.NewFilesServiceProvider(filesURL, grantTTL)

	mounts, err := toolfs.NewMountSet(toolfs.MountSetConfig{
		Root:         root,
		DefaultCaps:  toolfs.AgentCapabilities{AllowDelete: allowDelete},
		MountOptions: toolfs.MountConfig{Debug: debug},
	}, resolver, provider)
	if err != nil {
		return err
	}

	control := toolfs.NewControlServer(mounts, socketPath)
	if err := control.Listen(); err != nil {
		return fmt.Errorf("cannot listen on %s: %w", socketPath, err)
	}

	log.Printf("toolfsd: serving control API on %s, mount root %s", socketPath, root)
	if !allowDelete {
		log.Printf("toolfsd: delete is withheld by default; mounts are read-only " +
			"until the protection chain lands")
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- control.Serve() }()

	// Tear every mount down on shutdown. Leaving them behind would strand FUSE
	// mounts whose server is gone, which consumers see as ENOTCONN.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

// buildResolver wires the identity binding.
//
// The binding is required because the agent and ZimaOS run independent account
// systems: agent requests carry no ZimaOS credential, so the mapping has to be
// stated explicitly rather than derived from a token.
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
		log.Printf("toolfsd: no bindings and no fallback configured; every mount " +
			"request will be refused until accounts are bound")
	}

	return resolver, nil
}
