package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/devicestore"
)

// runTokenCLI implements `control token create -name <name> -type <type>
// [-ttl 24h] [-count N] [-out file]`, printing one-time device registration
// token(s). It talks directly to Redis rather than going through the HTTP
// API: token issuance is deliberately CLI-only for now, since gating it
// behind an HTTP endpoint would need admin auth (JWT roles), which doesn't
// exist until Phase 4. Shell access to this binary is the auth boundary in
// the meantime.
//
// -count/-out exist for Phase 7: cmd/fleet claims hundreds of synthetic
// devices, and running this CLI once per device isn't practical. With
// -count > 1, devices are named "<name>-00000", "<name>-00001", ... and
// tokens are written one per line (no other text) so cmd/fleet's token
// pool (cmd/fleet/manager.go's loadTokenPool) can read the file directly.
func runTokenCLI(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: control token create -name <name> [-type <type>] [-ttl 24h] [-count 1] [-out file]")
	}

	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	name := fs.String("name", "", "device display name (required); with -count > 1, used as a prefix")
	typ := fs.String("type", "generic", "device type, e.g. phone, laptop, esp32, fleet")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the token stays valid")
	count := fs.Int("count", 1, "number of tokens to issue")
	out := fs.String("out", "", "write tokens (one per line) to this file instead of stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("-name is required")
	}
	if *count < 1 {
		return fmt.Errorf("-count must be >= 1")
	}

	cfg := config.Load("control", defaultHTTPAddr)
	store := devicestore.New(cfg.RedisAddr)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		return fmt.Errorf("connecting to redis at %s: %w", cfg.RedisAddr, err)
	}

	if *count == 1 {
		token, err := store.CreateToken(ctx, *name, *typ, *ttl)
		if err != nil {
			return err
		}
		if *out == "" {
			fmt.Printf("registration token for %q (%s), valid %s:\n%s\n", *name, *typ, *ttl, token)
			return nil
		}
		return os.WriteFile(*out, []byte(token+"\n"), 0o600)
	}

	tokens := make([]string, 0, *count)
	for i := 0; i < *count; i++ {
		deviceName := fmt.Sprintf("%s-%05d", *name, i)
		token, err := store.CreateToken(ctx, deviceName, *typ, *ttl)
		if err != nil {
			return fmt.Errorf("issuing token %d/%d: %w", i+1, *count, err)
		}
		tokens = append(tokens, token)
	}

	var body string
	for _, t := range tokens {
		body += t + "\n"
	}
	if *out == "" {
		fmt.Print(body)
		return nil
	}
	if err := os.WriteFile(*out, []byte(body), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %d registration tokens (%s, valid %s) to %s\n", *count, *typ, *ttl, *out)
	return nil
}
