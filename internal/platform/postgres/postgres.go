// Package postgres инкапсулирует pgx pool и проверку версии схемы, без auto-migrate.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open создаёт pool; реальное подключение проверяет Check. Ограничения dial и
// размера предотвращают неограниченное накопление соединений при недоступной БД.
func Open(ctx context.Context, cfg config.Postgres, timeout time.Duration) (*pgxpool.Pool, error) {
	p, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errors.New("POSTGRES_CONFIG_INVALID")
	}
	p.MaxConns = cfg.MaxConns
	p.MinConns = 0
	p.ConnConfig.ConnectTimeout = timeout
	p.ConnConfig.RuntimeParams["search_path"] = "chat,public"
	p.MaxConnLifetime = time.Hour
	p.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, p)
	if err != nil {
		return nil, errors.New("POSTGRES_OPEN_FAILED")
	}
	return pool, nil
}

// Check проверяет сеть, версию и доступ к схеме без DDL/INSERT. Последняя запись
// каждого version_id учитывает goose down; неизвестные/пропущенные версии отвергаются.
func Check(ctx context.Context, pool *pgxpool.Pool) error {
	var valid bool
	err := pool.QueryRow(ctx, `WITH applied AS (
		SELECT DISTINCT ON (version_id) version_id,is_applied FROM public.goose_db_version ORDER BY version_id,id DESC
	) SELECT (SELECT count(*) FROM applied WHERE is_applied AND version_id>0)=$1
	AND NOT EXISTS (SELECT 1 FROM applied WHERE is_applied AND version_id>$1)
	AND has_schema_privilege(current_user,'chat','USAGE')`, migrations.Version).Scan(&valid)
	if err != nil || !valid {
		return errors.New("POSTGRES_SCHEMA_UNAVAILABLE")
	}
	return nil
}
