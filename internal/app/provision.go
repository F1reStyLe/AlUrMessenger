package app

import (
	"context"
	"flag"
	"fmt"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"time"
)

// ProvisionMain держит token output отдельно от diagnostics. Private key читается
// только dev-token командой, API и worker получают максимум public key.
func ProvisionMain(command string, args []string) int {
	if err := provisionRun(command, args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "provisioning failed; check command, environment and configuration")
		return 1
	}
	return 0
}

// provisionRun проверяет development guard до чтения private key или открытия БД.
func provisionRun(command string, args []string, out io.Writer) error {
	env := os.Getenv("APP_ENV")
	if env != "development" && env != "test" && env != "production" {
		return fmt.Errorf("APP_ENV required")
	}
	if (command == "seed" || command == "dev-token") && env != "development" {
		return fmt.Errorf("development required")
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if command == "dev-token" {
		subject := flags.String("subject", "alice", "external identity")
		admin := flags.Bool("admin", false, "Project admin role")
		key := flags.String("key", ".local/jwt-private.pem", "dev private key")
		if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
			return fmt.Errorf("invalid flags")
		}
		data, err := os.ReadFile(*key)
		if err != nil || len(data) > 16384 {
			return fmt.Errorf("invalid key")
		}
		token, err := provision.Token(env, data, *subject, *admin)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, token)
		return err
	}
	var p provision.Project
	if command == "project" {
		flags.StringVar(&p.ID, "id", "", "Project UUID")
		flags.StringVar(&p.Name, "name", "", "Project name")
		flags.StringVar(&p.Issuer, "issuer", "", "trusted HTTPS issuer")
		flags.StringVar(&p.Audience, "audience", "", "trusted audience")
	} else if command != "seed" {
		return fmt.Errorf("unknown command")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid flags")
	}
	if command == "project" {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	cfg, err := config.LoadPostgres(env, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, cfg.URL)
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	if command == "seed" {
		err = provision.Seed(ctx, env, db)
	} else {
		err = provision.Create(ctx, db, p)
	}
	if err == nil {
		_, err = fmt.Fprintln(out, "provisioning completed")
	}
	return err
}
