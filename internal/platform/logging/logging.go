// Package logging configures structured process logs.
package logging

import (
	"io"
	"log/slog"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// New создаёт logger с постоянной идентичностью процесса и фильтром уровня.
// cfg должен пройти config.Load. Функция не заменяет глобальный slog logger.
// Redaction не автоматическая: вызывающий код обязан передавать только безопасные поля.
func New(output io.Writer, cfg config.Log, service config.Service, environment string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	var handler slog.Handler = slog.NewJSONHandler(output, opts)
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(output, opts)
	}
	return slog.New(handler).With("service", string(service), "environment", environment)
}
