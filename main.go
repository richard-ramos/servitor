package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	redactor := NewRedactor(cfg.OpenAIAPIKey, cfg.TelegramBotToken)
	if err := cfg.ValidateForRun(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	authProvider, err := NewAuthProvider(cfg)
	if err != nil {
		return err
	}
	if err := authProvider.Validate(ctx); err != nil {
		return err
	}
	db, err := OpenDB(filepath.Join(cfg.DataDir, "servitor.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	proxy, err := NewCredentialProxy(cfg, redactor, authProvider)
	if err != nil {
		return err
	}
	if err := proxy.Start(); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Shutdown(shutdownCtx)
	}()
	runner := NewDockerRunner(cfg, redactor)
	if err := runner.BuildImage(ctx); err != nil {
		return err
	}
	app := &App{
		cfg:      cfg,
		db:       db,
		tg:       NewTelegramClient(cfg.TelegramBotToken),
		redactor: redactor,
		runner:   runner,
	}
	go app.RunQueueLoop(ctx)
	go app.RunSchedulerLoop(ctx)
	return app.PollLoop(ctx)
}
