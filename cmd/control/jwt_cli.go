package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/config"
)

// runJWTCLI implements `control jwt create -role admin [-ttl 1h]`,
// printing a signed bearer token. See auth.go's doc comment for why this
// exists instead of a login endpoint: Phase 4 has no password/user store,
// and this mirrors token.go's existing CLI-issuance pattern for exactly
// the same reason (shell access to this binary is the auth boundary).
func runJWTCLI(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: control jwt create -role <admin|operator|viewer> [-ttl 1h]")
	}

	fs := flag.NewFlagSet("jwt create", flag.ContinueOnError)
	role := fs.String("role", "", "admin, operator, or viewer (required)")
	ttl := fs.Duration("ttl", time.Hour, "how long the token stays valid")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if _, ok := roleRank[*role]; !ok {
		return fmt.Errorf("-role must be one of admin, operator, viewer")
	}

	cfg := config.Load("control", defaultHTTPAddr)
	if cfg.JWTSigningKey == "" {
		return fmt.Errorf("JWT_SIGNING_KEY is not set")
	}

	token, err := signToken(*role, cfg.JWTIssuer, *ttl, []byte(cfg.JWTSigningKey), "")
	if err != nil {
		return err
	}
	fmt.Printf("%s token, valid %s:\n%s\n", *role, *ttl, token)
	return nil
}
