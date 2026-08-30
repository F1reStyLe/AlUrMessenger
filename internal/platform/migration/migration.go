// Package migration применяет embedded SQL только по явной команде оператора.
package migration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Run поддерживает только up/status. Session advisory lock сериализует конкурентные
// migrators; SQL исполняется goose в транзакциях. Runtime не вызывает этот пакет.
// Goose diagnostics отключены: upstream errors могут включать SQL или credentials.
func Run(ctx context.Context, rawURL, action string) (int64, error) {
	if action != "up" && action != "status" {
		return 0, errors.New("MIGRATION_ACTION_INVALID")
	}
	cfg, err := pgx.ParseConfig(rawURL)
	if err != nil {
		return 0, errors.New("MIGRATION_CONFIG_INVALID")
	}
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams["search_path"] = "public"
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	db.SetMaxOpenConns(2)
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 30), lock.WithUnlockTimeout(1, 2))
	if err != nil {
		return 0, errors.New("MIGRATION_LOCK_CONFIG_INVALID")
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true),
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		return 0, errors.New("MIGRATION_PROVIDER_FAILED")
	}
	if action == "up" {
		if _, err = p.Up(ctx); err != nil {
			return 0, errors.New("MIGRATION_UP_FAILED")
		}
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, errors.New("MIGRATION_STATUS_FAILED")
	}
	if action == "up" && v != migrations.Version {
		return v, errors.New("MIGRATION_VERSION_MISMATCH")
	}
	return v, nil
}
