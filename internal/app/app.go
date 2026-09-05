// Package app is the composition root shared by the API and worker binaries.
package app

import (
	"context"
	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	attachmentrepo "github.com/F1reStyLe/AlUrMessenger/internal/attachment/repository"
	attachmenthttp "github.com/F1reStyLe/AlUrMessenger/internal/attachment/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	conversationhttp "github.com/F1reStyLe/AlUrMessenger/internal/conversation/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	messagehttp "github.com/F1reStyLe/AlUrMessenger/internal/message/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/outbox"
	"github.com/F1reStyLe/AlUrMessenger/internal/realtime"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/apidocs"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/logging"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	policyrepo "github.com/F1reStyLe/AlUrMessenger/internal/policy/repository"
	policyhttp "github.com/F1reStyLe/AlUrMessenger/internal/policy/transport"
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
func run(ctx context.Context, service config.Service, output io.Writer) (exitCode int) {
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
	// Валидируем секреты/адреса до bind; конфигурация мигратора сюда не загружается.
	infraConfig, err := config.LoadInfrastructure(cfg.Environment)
	if err != nil {
		logger.Error("infrastructure configuration rejected", "error", err.Error())
		return 1
	}
	securityConfig, err := admission.Load(cfg.Environment)
	if err != nil {
		logger.Error("admission configuration rejected")
		return 1
	}
	var verifier auth.Verifier
	var contentKeys *cryptography.Keys
	allowNoOrigin := false
	if service == config.API {
		// Remote identity validation is the normal mode. Offline RSA is an explicit
		// development fixture only and cannot silently bypass session checks in production.
		switch os.Getenv("AUTH_MODE") {
		case "", "remote":
			verifier, err = auth.NewRemote(os.Getenv("AUTH_BASE_URL"), os.Getenv("AUTH_PROJECT_ID"), cfg.Environment, nil)
		case "dev-rsa":
			if cfg.Environment == "development" || cfg.Environment == "test" {
				verifier, err = auth.Load(os.Getenv("AUTH_PUBLIC_KEY_FILE"), nil)
			} else {
				err = auth.ErrUnauthenticated
			}
		default:
			err = auth.ErrUnauthenticated
		}
		if err != nil {
			logger.Error("authentication configuration rejected")
			return 1
		}
		contentKeys, err = cryptography.Load(os.Getenv("CONTENT_KEYS_FILE"))
		if err != nil {
			logger.Error("content encryption configuration rejected")
			return 1
		}
		switch os.Getenv("WS_ALLOW_NO_ORIGIN") {
		case "", "false":
		case "true":
			allowNoOrigin = true
		default:
			logger.Error("websocket configuration rejected")
			return 1
		}
	}
	listener, err := net.Listen("tcp", cfg.HTTP.Address)
	if err != nil {
		// Системная ошибка может содержать детали окружения; наружу идёт только код.
		logger.Error("http listener failed", "error_code", "LISTEN_FAILED")
		return 1
	}
	// На ошибке startup порт освобождается даже до передачи listener в server.Run.
	defer listener.Close()
	clients, err := infrastructure.Open(ctx, infraConfig)
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		logger.Error("infrastructure startup failed", "error_code", err.Error())
		return 1
	}
	// HTTP drain завершается раньше закрытия зависимостей. Background context нужен,
	// потому что signal context на этой стадии уже отменён. Cleanup имеет свой budget.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := clients.Close(cleanup); err != nil {
			logger.Error("infrastructure shutdown failed", "error_code", err.Error())
			exitCode = 1
		} else if exitCode == 0 {
			// Штатное завершение подтверждаем только после освобождения зависимостей.
			logger.Info("service stopped")
		}
	}()
	server := httpserver.New(cfg.HTTP, cfg.ShutdownTimeout, logger, clients.Check)
	if service == config.API {
		store := &repository.Store{DB: clients.Postgres}
		switch v := verifier.(type) {
		case *auth.Remote:
			verifier = v.WithProjects(store)
		case *auth.RSA:
			verifier = v.WithProjects(store)
		}
		identities := &identity.Service{Verifier: verifier, Store: store}
		limiter := &admission.Limiter{Redis: clients.Redis, Config: securityConfig}
		// Auth owns login/refresh/logout. Chat accepts its bearer token directly and
		// resolves current permissions from its own DB after each identity check.
		// Order: IP/CORS → JWT/provisioning → Project-user limit → permissions/use case.
		protect := func(next http.Handler) http.Handler {
			return limiter.Before(identityhttp.Authenticate(identities, limiter.After(next)))
		}
		identityhttp.Register(server, identities, protect)
		attachments := attachment.New(&attachmentrepo.Store{DB: clients.Postgres}, clients.Storage, cfg.Upload)
		attachmenthttp.Register(server, attachments, protect)
		messageStore := &messagerepo.Store{DB: clients.Postgres, Crypto: contentKeys}
		messages := &message.Service{Store: messageStore}
		recovery := &message.RecoveryService{Store: messageStore}
		conversations := &conversation.Service{Store: &conversationrepo.Store{DB: clients.Postgres}}
		messagehttp.Register(server, messages, protect)
		messagehttp.RegisterRecovery(server, recovery, protect)
		conversationhttp.Register(server, conversations, protect)
		conversationhttp.RegisterMembership(server, &conversation.MembershipService{Store: &conversationrepo.Store{DB: clients.Postgres}}, protect)
		policyhttp.Register(server, &policy.Service{Store: &policyrepo.Store{DB: clients.Postgres}}, protect)
		apidocs.Register(server)
		hub := realtime.NewHub()
		gateway := &realtime.Gateway{Hub: hub, Identity: identities, Messages: messages, Recovery: recovery, Conversations: conversations, Presence: &realtime.Presence{DB: clients.Postgres, Redis: clients.Redis}, Limiter: limiter, AllowNoOrigin: allowNoOrigin}
		server.Handle("/ws", limiter.Before(gateway))
		background, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); hub.Listen(background, clients.Redis) }()
		// Trigger drain with process shutdown, and also on an early HTTP serving error.
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			select {
			case <-ctx.Done():
			case <-background.Done():
			}
			hub.Drain()
		}()
		defer func() { cancel(); <-drainDone; <-done }()
	} else {
		router, routerErr := outbox.NewRouter(infraConfig.Kafka, clients.Postgres, clients.Redis)
		if routerErr != nil {
			logger.Error("event router configuration rejected")
			return 1
		}
		// Stop publication before closing the DB/Kafka clients, including HTTP startup failure.
		background, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		routerDone := make(chan struct{})
		presenceDone := make(chan struct{})
		attachmentDone := make(chan struct{})
		go func() {
			defer close(attachmentDone)
			(&attachmentrepo.Cleaner{DB: clients.Postgres, Blobs: clients.Storage, Logger: logger}).Run(background)
		}()
		go func() {
			defer close(presenceDone)
			(&realtime.Presence{DB: clients.Postgres, Redis: clients.Redis}).RunFlush(background)
		}()
		go func() { defer close(routerDone); router.Run(background) }()
		go func() {
			defer close(done)
			(&outbox.Worker{DB: clients.Postgres, Publisher: outbox.Kafka{Client: clients.Kafka}, Logger: logger}).Run(background)
		}()
		defer func() { cancel(); <-done; <-routerDone; <-presenceDone; <-attachmentDone }()
	}
	logger.Info("service starting")
	if err := server.Run(ctx, listener); err != nil {
		logger.Error("service stopped with error", "error_code", "LIFECYCLE_FAILED")
		return 1
	}
	return 0
}
