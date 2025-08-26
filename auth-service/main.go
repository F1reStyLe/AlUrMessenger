package main

import (
	"auth/auth"
	"auth/config"
	"auth/database"
	"auth/handlers"
	"auth/repository"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	migrate "github.com/rubenv/sql-migrate"
)

var (
	jwtKey []byte
	db     *sql.DB
)

func main() {
	cfg := config.MustLoad()

	jwtKey = []byte(cfg.JWT.SecretKey)

	// Подключение к БД
	var err error
	db, err := database.NewPostgresConnection(cfg.Database)
	if err != nil {
		log.Fatal("❌ Failed to connect to database")
	}
	defer db.Close()

	log.Printf("✅ Database connected")

	// Проверяем соединение
	if err = db.Ping(); err != nil {
		log.Fatal("Failed to ping database:", err)
	}

	// Настройка миграций
	migrations := &migrate.FileMigrationSource{
		Dir: "migrations",
	}

	// Используем db.DB — это *sql.DB
	n, err := migrate.Exec(db.DB, "postgres", migrations, migrate.Up)
	if err != nil {
		log.Fatal("Failed to apply migrations:", err)
	}
	log.Printf("Applied %d migrations!\n", n)

	userRepo := repository.NewPostgresUserRepository(db)
	tokenRepo := repository.NewPostgresTokenRepository(db)

	authService := auth.NewAuthService(userRepo, tokenRepo, cfg.JWT.SecretKey, cfg.JWT.AccessTokenTTL, cfg.JWT.RefreshTokenTTL)

	authHandler := handlers.NewAuthHandler(authService)

	r := mux.NewRouter()

	// Эндпоинты
	r.HandleFunc("/register", authHandler.Register).Methods("POST")
	r.HandleFunc("/login", authHandler.Login).Methods("POST")
	r.HandleFunc("/logout", authHandler.Logout).Methods("POST")
	r.HandleFunc("/me", authHandler.WhoAmI).Methods("GET")
	r.HandleFunc("/refresh", authHandler.Refresh).Methods("POST")

	// Запуск сервера
	port := fmt.Sprintf(":%s", cfg.Http.Port)
	fmt.Printf("Auth service is running on %s...", port)
	log.Fatal(http.ListenAndServe(":8085", r))
}
