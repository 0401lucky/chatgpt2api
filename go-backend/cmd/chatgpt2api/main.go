package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"chatgpt2api-go-backend/internal/account"
	"chatgpt2api-go-backend/internal/auth"
	"chatgpt2api-go-backend/internal/config"
	"chatgpt2api-go-backend/internal/httpapi"
	"chatgpt2api-go-backend/internal/proxy"
	"chatgpt2api-go-backend/internal/storage"
	"chatgpt2api-go-backend/internal/upstream"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	store := storage.NewJSONStore(cfg.DataDir)
	accounts, err := account.NewService(store, cfg.ImageAccountConcurrency)
	if err != nil {
		log.Fatalf("load accounts: %v", err)
	}
	authService := auth.NewService(store, cfg.AuthKey)
	proxyService := proxy.NewService(cfg.Proxy)
	upstreamService := upstream.NewService(accounts, proxyService)
	if cfg.BaseURL != "" {
		upstreamService.BaseURL = cfg.BaseURL
	}
	upstreamService.ImagePollTimeout = time.Duration(cfg.ImagePollTimeoutSecs) * time.Second
	upstreamService.ImagePollInitialWait = time.Duration(cfg.ImagePollInitialWaitSecs) * time.Second
	upstreamService.ImagePollInterval = time.Duration(cfg.ImagePollIntervalSecs) * time.Second
	accounts.SetRemoteRefresher(upstreamService)
	app := httpapi.New(cfg, accounts, authService, upstreamService)

	port := strings.TrimSpace(os.Getenv("CHATGPT2API_GO_PORT"))
	if port == "" {
		port = "8001"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("go backend listening on http://127.0.0.1:%s", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
