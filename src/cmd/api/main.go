package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"alurmsg/internal/config"
	"alurmsg/pkg/database"

	"github.com/jmoiron/sqlx"

	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file" // ← Регистрирует file:// драйвер!
)

func main() {
	logger := setupLogger()
	// Загрузка конфигурации
	cfg := config.MustLoad()
	logger.Info("✅ Config loaded")

	// Подключение к БД
	db, err := database.NewPostgresConnection(cfg.Database)
	if err != nil {
		logger.Error("❌ Failed to connect to database")
	}
	defer db.Close()
	logger.Info("✅ Database connected")

	// Инициализация зависимостей (пока заглушки)
	initServices(db, cfg, logger)

	// Graceful shutdown
	waitForShutdown(logger)
}

func setupLogger() *slog.Logger {
	// Настройка JSON логгера для продакшена, текстового для разработки
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		logLevel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	var handler slog.Handler
	if os.Getenv("ENV") == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func initServices(db *sqlx.DB, cfg *config.Config, logger *slog.Logger) {
	logger.Info("📋 Initializing services...")

	// Здесь будет инициализация репозиториев и сервисов
	// Пока просто логируем конфиг
	logger.Debug("Configuration",
		"environment", cfg.Environment,
		"http_host", cfg.HTTP.Host,
		"http_port", cfg.HTTP.Port,
		"db_host", cfg.Database.Host,
	)

	// TODO: Инициализация репозиториев
	// repos := repository.NewRepository(db, logger)

	// TODO: Инициализация сервисов
	// services := service.NewService(service.Deps{
	//     Repos:          repos,
	//     Logger:         logger,
	//     JWTSecret:      cfg.JWT.SecretKey,
	//     AccessTokenTTL: cfg.JWT.AccessTokenTTL,
	// })

	logger.Info("✅ Services initialized")
}

func waitForShutdown(logger *slog.Logger) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("🛑 Shutting down...")
	time.Sleep(1 * time.Second)
}
