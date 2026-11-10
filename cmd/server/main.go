// Command server runs the devcard BFF HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deepanshusarswat/devcard-bff/internal/cache"
	"github.com/deepanshusarswat/devcard-bff/internal/config"
	"github.com/deepanshusarswat/devcard-bff/internal/devcard"
	"github.com/deepanshusarswat/devcard-bff/internal/github"
	"github.com/deepanshusarswat/devcard-bff/internal/health"
	"github.com/deepanshusarswat/devcard-bff/internal/middleware"
)

func main() {
	// main stays tiny: all real work happens in run, which returns an error
	// instead of calling os.Exit. That keeps deferred cleanups running.
	if err := run(); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// ctx is cancelled on the first SIGINT (Ctrl-C) or SIGTERM (docker stop,
	// Kubernetes pod termination).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// ContextHandler stamps request_id onto every log line written with a request context.
	logger := slog.New(middleware.ContextHandler{
		Handler: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}),
	})
	slog.SetDefault(logger)

	ghOpts := []github.Option{github.WithBaseURL(cfg.GitHubBaseURL)}
	if cfg.GitHubToken != "" {
		ghOpts = append(ghOpts, github.WithToken(cfg.GitHubToken))
	} else {
		logger.Warn("GITHUB_TOKEN not set: limited to 60 unauthenticated GitHub requests per hour")
	}
	ghClient := github.NewClient(ghOpts...)

	cards := cache.New[string, devcard.Card](cfg.CacheTTL, cache.WithMaxEntries(10_000))
	go cards.RunJanitor(ctx, time.Minute) // stops when ctx is cancelled at shutdown

	svc := devcard.NewService(ghClient, cards, cfg.UpstreamTimeout)

	checker := health.New()
	mux := http.NewServeMux()
	checker.Register(mux)
	devcard.NewHandler(svc, logger).Register(mux)

	// Outermost first: every request gets an ID, then is logged, then is
	// protected from panics; CORS sits next to the router to answer preflights.
	handler := middleware.Chain(mux,
		middleware.RequestID,
		middleware.Logger(logger),
		middleware.Recover(logger),
		middleware.CORS(cfg.AllowedOrigin),
	)

	srv := &http.Server{
		Addr:    net.JoinHostPort("", cfg.Port),
		Handler: handler,
		// Never run a production server without timeouts: the zero values mean
		// "wait forever", which lets slow clients exhaust connections.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.UpstreamTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	return serve(ctx, srv, checker, cfg.ShutdownTimeout, logger)
}

// serve runs srv until ctx is cancelled, then shuts it down gracefully.
func serve(ctx context.Context, srv *http.Server, checker *health.Checker, shutdownTimeout time.Duration, logger *slog.Logger) error {
	// Buffered so the goroutine can always send and exit, even if we have
	// already stopped listening on the channel.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received, draining")
	checker.SetReady(false)

	// A fresh context: the signal ctx is already cancelled, and we want to
	// give in-flight requests up to shutdownTimeout to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("server stopped cleanly")
	return nil
}
