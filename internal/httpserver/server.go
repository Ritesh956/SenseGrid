// Package httpserver provides the health-check HTTP server and graceful
// shutdown lifecycle shared by every SenseGrid service.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// New builds an *http.Server exposing GET /healthz on addr. Callers may
// register additional routes on Mux before passing the server to Run.
func New(addr string) (*http.Server, *http.ServeMux) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{Addr: addr, Handler: mux}, mux
}

// Run starts srv, blocks until ctx is cancelled (e.g. by a SIGTERM), then
// shuts down within shutdownTimeout. Returns any error other than a clean
// shutdown or a normal listener close. When certFile and keyFile are both
// set, srv terminates TLS itself instead of serving plain HTTP — used by
// cmd/control so a phone browser gets the secure context it needs for the
// DeviceMotion/DeviceOrientation APIs.
func Run(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration, certFile, keyFile string, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" && keyFile != "" {
			logger.Info("https server starting", "addr", srv.Addr)
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			logger.Info("http server starting", "addr", srv.Addr)
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining http server", "timeout", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}
