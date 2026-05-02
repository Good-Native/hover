package observability

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestStartMetricsServerServesMetrics(t *testing.T) {
	addr := freePort(t)

	srv, err := StartMetricsServer(context.Background(), MetricsServerOptions{
		ServiceName:    "hover-test",
		Environment:    "test",
		MetricsAddress: addr,
	})
	if err != nil {
		t.Fatalf("StartMetricsServer: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	// Server boots in a goroutine; poll briefly rather than racing with Serve.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/metrics")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("metrics endpoint never came up: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "go_") {
		t.Error("expected default Go collectors in /metrics output")
	}
}

func TestStartMetricsServerPprofGated(t *testing.T) {
	addr := freePort(t)

	srv, err := StartMetricsServer(context.Background(), MetricsServerOptions{
		ServiceName:    "hover-test",
		Environment:    "test",
		MetricsAddress: addr,
		EnablePprof:    false,
	})
	if err != nil {
		t.Fatalf("StartMetricsServer: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/debug/pprof/")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("pprof should be 404 when EnablePprof=false, got %d", resp.StatusCode)
	}
}

func TestStartMetricsServerShutdownIdempotent(t *testing.T) {
	addr := freePort(t)

	srv, err := StartMetricsServer(context.Background(), MetricsServerOptions{
		ServiceName:    "hover-test",
		Environment:    "test",
		MetricsAddress: addr,
	})
	if err != nil {
		t.Fatalf("StartMetricsServer: %v", err)
	}

	srv.Shutdown(context.Background())
	srv.Shutdown(context.Background()) // must not panic

	var nilServer *MetricsServer
	nilServer.Shutdown(context.Background()) // must not panic
}

func TestStartMetricsServerNoAddress(t *testing.T) {
	srv, err := StartMetricsServer(context.Background(), MetricsServerOptions{
		ServiceName: "hover-test",
		Environment: "test",
	})
	if err != nil {
		t.Fatalf("StartMetricsServer: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	if srv.httpSrv != nil {
		t.Error("no HTTP server should be started when MetricsAddress is empty")
	}
}
