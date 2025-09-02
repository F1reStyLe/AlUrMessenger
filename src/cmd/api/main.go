package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"alurmsg/internal/auth"
	"alurmsg/internal/config"
	"alurmsg/internal/domain/http/websocket"
	"alurmsg/pkg/database"

	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	// Загрузка конфигурации
	cfg := config.MustLoad()

	logger := setupLogger(cfg)
	logger.Info("✅ Config loaded")

	// Подключение к БД
	db, err := database.NewPostgresConnection(cfg.Database)
	if err != nil {
		logger.Error("❌ Failed to connect to database")
	}
	defer db.Close()
	logger.Info("✅ Database connected")

	// Инициализация WebSocket хаба
	wsHub := websocket.New()
	wsHub.Start()

	// Настройка HTTP маршрутов
	http.HandleFunc("/ws", auth.AuthMiddleware([]byte(cfg.JWT.SecretKey))(websocket.HandleWebSocket))
	http.HandleFunc("/health", healthCheckHandler)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Shutdown signal received")
		wsHub.GracefulShutdown()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	port := fmt.Sprintf(":%s", cfg.HTTP.Port)
	// Запуск сервера
	logger.Info(fmt.Sprintf("Server starting on %s", port))
	if err := http.ListenAndServe(port, nil); err != nil {
		logger.Info("Server failed to start")
	}

	// Graceful shutdown
	waitForShutdown(logger)
}

func setupLogger(cfg *config.Config) *slog.Logger {
	// Настройка логгера.
	var logLevel slog.Level

	switch cfg.Environment {
	case "debug":
		logLevel = slog.LevelDebug
	case "local":
		logLevel = slog.LevelWarn
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	var handler slog.Handler

	switch cfg.Environment {
	case "debug":
		handler = slog.NewTextHandler(os.Stdout, opts)
	case "local":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func waitForShutdown(logger *slog.Logger) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("🛑 Shutting down...")
	time.Sleep(1 * time.Second)
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok", "websocket_clients": 0}`))
}
