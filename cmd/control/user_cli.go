package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/users"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runUserCLI implements `control user create -username <u> -role
// <admin|operator|viewer> -password <p>`, the CLI-only provisioning path
// for console logins — mirroring token.go/jwt_cli.go's existing pattern
// (shell access to this binary is the auth boundary) rather than adding a
// self-service signup endpoint nothing in this project needs.
func runUserCLI(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: control user create -username <u> -role <admin|operator|viewer> -password <p>")
	}

	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	username := fs.String("username", "", "console login username (required)")
	role := fs.String("role", "", "admin, operator, or viewer (required)")
	password := fs.String("password", "", "console login password (required)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("-username is required")
	}
	if _, ok := roleRank[*role]; !ok {
		return fmt.Errorf("-role must be one of admin, operator, viewer")
	}
	if *password == "" {
		return fmt.Errorf("-password is required")
	}

	cfg := config.Load("control", defaultHTTPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	store := users.New(pool)
	u, err := store.Create(ctx, *username, string(hash), *role)
	if err != nil {
		return err
	}
	fmt.Printf("created %s user %q (id %s)\n", u.Role, u.Username, u.ID)
	return nil
}
