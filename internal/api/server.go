package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Server lifecycle timeouts. ReadHeaderTimeout guards against slowloris;
// shutdownTimeout bounds how long Start waits for in-flight requests to
// drain before force-closing.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// Server owns the *http.Server lifecycle plus its ServeMux. It is
// intentionally minimal: Start blocks until ctx is cancelled, then performs
// a graceful shutdown bounded by shutdownTimeout.
type Server struct {
	addr string
	mux  *http.ServeMux
	log  *slog.Logger
	srv  *http.Server
}

// NewServer builds the mux, registers handlers, and wires a ready-to-run
// http.Server. The caller drives lifecycle via Start.
func NewServer(addr string, handlers *Handlers, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	handlers.Register(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	return &Server{
		addr: addr,
		mux:  mux,
		log:  log,
		srv:  srv,
	}
}

// Start runs the HTTP server until ctx is cancelled. On cancellation it
// calls Shutdown with a shutdownTimeout-bound context so in-flight requests
// have a chance to finish cleanly. http.ErrServerClosed is swallowed since
// it is the expected outcome of a graceful stop.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("api: listening", slog.String("addr", s.addr))
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("api: listen: %w", err)
		}
		return nil
	case <-ctx.Done():
		s.log.Info("api: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("api: shutdown: %w", err)
		}
		// Drain the goroutine so we do not leak it.
		<-errCh
		return nil
	}
}
