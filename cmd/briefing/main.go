package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"finkoper-morning-briefing/internal/briefing"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service_stopped", "error", err.Error())
		os.Exit(1)
	}
}
func run() error {
	cfg, err := briefing.LoadConfig()
	if err != nil {
		return err
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	probeSystem, probeUser := briefing.SyntheticProbe()
	if err = counter.Verify(context.Background(), probeSystem, probeUser); err != nil {
		return err
	}
	store, err := briefing.Open(cfg.DB)
	if err != nil {
		return errors.New("cannot open or lock database")
	}
	defer store.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("cannot listen")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &http.Server{Handler: briefing.Handler(store, cfg.Token, counter), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: counter.Timeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- briefing.Run(workerCtx, store, briefing.NewOllama(cfg.BaseURL, cfg.Model, cfg.Timeout, counter))
	}()
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(listener) }()
	slog.Info("service_started")
	workerExited := false
	select {
	case <-ctx.Done():
	case err = <-workerDone:
		workerExited = true
	case serveErr := <-serverDone:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.New("http_server_failed")
		}
	}
	shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	if e := srv.Shutdown(shutdownCtx); e != nil {
		_ = srv.Close()
	}
	cancel()
	if !workerExited {
		if e := <-workerDone; err == nil {
			err = e
		}
	}
	slog.Info("service_stopped")
	return err
}
