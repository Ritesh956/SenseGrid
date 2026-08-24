package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/devicestore"
)

// runTokenCLI implements `control token create -name <name> -type <type>
// [-ttl 24h]`, printing a one-time device registration token. It talks
// directly to Redis rather than going through the HTTP API: token issuance
// is deliberately CLI-only for now, since gating it behind an HTTP endpoint
// would need admin auth (JWT roles), which doesn't exist until Phase 4.
// Shell access to this binary is the auth boundary in the meantime.
func runTokenCLI(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: control token create -name <name> [-type <type>] [-ttl 24h]")
	}

	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	name := fs.String("name", "", "device display name (required)")
	typ := fs.String("type", "generic", "device type, e.g. phone, laptop, esp32")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the token stays valid")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("-name is required")
	}

	cfg := config.Load("control", defaultHTTPAddr)
	store := devicestore.New(cfg.RedisAddr)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		return fmt.Errorf("connecting to redis at %s: %w", cfg.RedisAddr, err)
	}

	token, err := store.CreateToken(ctx, *name, *typ, *ttl)
	if err != nil {
		return err
	}
	fmt.Printf("registration token for %q (%s), valid %s:\n%s\n", *name, *typ, *ttl, token)
	return nil
}
