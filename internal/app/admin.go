package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/logging"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/migration"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/objectstore"
)

// AdminMain выполняет только явно выбранную административную операцию с deadline.
// Credentials передаются через environment/secret files, никогда через CLI arguments.
func AdminMain(command string, args []string) int {
	env := os.Getenv("APP_ENV")
	logger := logging.New(os.Stdout, config.Log{Format: "json"}, config.Service(command), "administration")
	if env != "development" && env != "test" && env != "production" {
		logger.Error("APP_ENV is required")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch command {
	case "migrate":
		if len(args) != 1 || args[0] != "up" && args[0] != "status" {
			logger.Error("usage: migrate up|status")
			return 1
		}
		cfg, err := config.LoadPostgres(env, true)
		if err != nil {
			logger.Error("configuration rejected", "error", err.Error())
			return 1
		}
		version, err := migration.Run(ctx, cfg.URL, args[0])
		if err != nil {
			logger.Error("migration failed", "error_code", err.Error())
			return 1
		}
		logger.Info("migration completed", "schema_version", version)
		return 0
	case "minio-init":
		if len(args) != 0 {
			logger.Error("usage: minio-init")
			return 1
		}
		cfg, err := config.LoadStorage(env)
		if err != nil {
			logger.Error("configuration rejected", "error", err.Error())
			return 1
		}
		store, err := objectstore.Open(cfg, 5*time.Second)
		if err != nil {
			logger.Error("storage configuration rejected")
			return 1
		}
		defer store.Close()
		if err = store.Initialize(ctx); err != nil {
			logger.Error("bucket initialization failed", "error_code", err.Error())
			return 1
		}
		logger.Info("private bucket verified")
		return 0
	default:
		logger.Error("unknown administrative command")
		return 1
	}
}
