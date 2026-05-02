package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// MetricsServerOptions bundles the observability config and HTTP listener
// settings that all three Fly binaries reproduce verbatim.
type MetricsServerOptions struct {
	ServiceName    string
	Environment    string
	OTLPEndpoint   string
	OTLPHeaders    map[string]string
	OTLPInsecure   bool
	MetricsAddress string
	EnablePprof    bool         // worker + analysis enable pprof; cmd/app does not.
	Logger         *slog.Logger // optional; defaults to slog.Default().
}

// MetricsServer ties the OTel providers to the metrics HTTP server so the
// caller manages a single Shutdown.
type MetricsServer struct {
	Providers *Providers

	httpSrv *http.Server
	log     *slog.Logger
}

// StartMetricsServer initialises the OTel providers and, when the providers
// expose a Prometheus handler and MetricsAddress is set, binds /metrics
// (plus /debug/pprof when EnablePprof is true) on that address.
//
// Telemetry is treated as best-effort: a bind failure is logged but does not
// return an error, since the rest of the binary should still come up. Init
// failures are returned because they imply a malformed config.
func StartMetricsServer(ctx context.Context, opts MetricsServerOptions) (*MetricsServer, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "metrics")

	providers, err := Init(ctx, Config{
		Enabled:        true,
		ServiceName:    opts.ServiceName,
		Environment:    opts.Environment,
		OTLPEndpoint:   opts.OTLPEndpoint,
		OTLPHeaders:    opts.OTLPHeaders,
		OTLPInsecure:   opts.OTLPInsecure,
		MetricsAddress: opts.MetricsAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("observability init: %w", err)
	}

	s := &MetricsServer{Providers: providers, log: log}

	if providers == nil || providers.MetricsHandler == nil || opts.MetricsAddress == "" {
		return s, nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", providers.MetricsHandler)
	if opts.EnablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	srv := &http.Server{
		Addr:              opts.MetricsAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", opts.MetricsAddress)
	if err != nil {
		log.Error("metrics server failed to bind", "error", err, "addr", opts.MetricsAddress)
		return s, nil
	}

	s.httpSrv = srv
	go func() {
		log.Info("metrics server listening", "addr", opts.MetricsAddress)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server failed", "error", err)
		}
	}()

	return s, nil
}

// Shutdown stops the HTTP server (5s grace) and flushes the OTel providers
// (10s grace, inherited from Providers.Shutdown). Safe on a nil receiver and
// after a partial init.
func (s *MetricsServer) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}

	if s.httpSrv != nil {
		httpCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.httpSrv.Shutdown(httpCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("graceful shutdown of metrics server failed", "error", err)
		}
		cancel()
	}

	if s.Providers != nil && s.Providers.Shutdown != nil {
		if err := s.Providers.Shutdown(ctx); err != nil {
			s.log.Warn("failed to flush telemetry providers", "error", err)
		}
	}
}
