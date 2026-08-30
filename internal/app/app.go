// Package app is the composition root shared by the API and worker binaries.
package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/logging"
)

// Main returns an exit status after all lifecycle cleanup has completed.
func Main(service config.Service) int {
	// Сигналы управляют lifecycle, но не отменяют контексты активных HTTP-запросов
	// напрямую: сервер предоставляет им отдельное окно graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, service, os.Stdout)
}

// run собирает процесс в порядке config → logger → listener → server.
// Отдельные context/output позволяют проверять запуск без сигналов и глобального stdout.
// После передачи listener в Run его закрытие становится ответственностью сервера.
func run(ctx context.Context, service config.Service, output io.Writer) int {
	cfg, err := config.Load(service)
	if err != nil {
		// Config diagnostics never include supplied environment values.
		logging.New(output, config.Log{Level: slog.LevelInfo, Format: "json"}, service, "unconfigured").
			Error("configuration rejected", "error", err.Error())
		return 1
	}
	logger := logging.New(output, cfg.Log, service, cfg.Environment)
	// Уже отменённый запуск не должен кратковременно занимать порт.
	if ctx.Err() != nil {
		return 0
	}
	listener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		// Системная ошибка может содержать детали окружения; наружу идёт только код.
		logger.Error("http listener failed", "error_code", "LISTEN_FAILED")
		return 1
	}
	// No business handlers/jobs are installed until their phase is implemented.
	// Nil readiness accurately reports that the service is not yet configured.
	server := httpserver.New(cfg.HTTP, cfg.ShutdownTimeout, logger, nil)
	logger.Info("service starting", "capability", "process_probes_only")
	if err := server.Run(ctx, listener); err != nil {
		logger.Error("service stopped with error", "error_code", "LIFECYCLE_FAILED")
		return 1
	}
	logger.Info("service stopped")
	return 0
}
