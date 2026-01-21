// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var version = "dev"

func main() {
	// Parse command-line flags
	transport := flag.String("transport", "stdio", "Transport mode: stdio, sse, or http")
	port := flag.Int("port", 8080, "Port to listen on (for sse and http transports)")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "text", "Log format: text, json")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("kube-compare-mcp %s\n", version)
		os.Exit(0)
	}

	// Initialize logger
	logger := initLogger(*logLevel, *logFormat)
	slog.SetDefault(logger)

	logger.Info("Starting kube-compare-mcp",
		"version", version,
		"transport", *transport,
		"logLevel", *logLevel,
	)

	// Create the MCP server with build-time version
	s := mcpserver.NewServer(version)

	switch *transport {
	case "stdio":
		runStdioServer(s, logger)
	case "sse":
		runSSEServer(s, *port, logger)
	case "http":
		runHTTPServer(s, *port, logger)
	default:
		logger.Error("Unknown transport", "transport", *transport)
		os.Exit(1)
	}
}

// initLogger creates a slog.Logger with the specified level and format.
func initLogger(level, format string) *slog.Logger {
	// Parse log level
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: slogLevel,
	}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

// runStdioServer starts the server using stdio transport (standard for local MCP)
func runStdioServer(s *mcp.Server, logger *slog.Logger) {
	logger.Debug("Starting stdio transport")
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Error("Server error", "error", err)
		os.Exit(1)
	}
}

// runSSEServer starts the server using SSE (Server-Sent Events) transport
func runSSEServer(s *mcp.Server, port int, logger *slog.Logger) {
	addr := fmt.Sprintf(":%d", port)
	logger.Info("Starting SSE server",
		"addr", addr,
		"sseEndpoint", fmt.Sprintf("http://localhost:%d/sse", port),
		"messageEndpoint", fmt.Sprintf("http://localhost:%d/message", port),
		"healthEndpoint", fmt.Sprintf("http://localhost:%d/health", port),
	)

	// Create a mux to handle both SSE and health endpoints
	mux := http.NewServeMux()

	// Health endpoint for Kubernetes liveness/readiness probes
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// SSE endpoints handled by the MCP SSE handler
	sseHandler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux.Handle("/sse", sseHandler)
	mux.Handle("/message", sseHandler)

	// Wrap with logging middleware
	handler := loggingMiddleware(mux, logger)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // Longer timeout for SSE streaming
		IdleTimeout:       120 * time.Second,
	}

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigChan

		logger.Info("Received shutdown signal", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("Error during shutdown", "error", err)
		}
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("SSE server error", "error", err)
		os.Exit(1)
	}
	logger.Info("Server stopped")
}

// runHTTPServer starts the server using Streamable HTTP transport
func runHTTPServer(s *mcp.Server, port int, logger *slog.Logger) {
	addr := fmt.Sprintf(":%d", port)
	logger.Info("Starting HTTP server",
		"addr", addr,
		"mcpEndpoint", fmt.Sprintf("http://localhost:%d/mcp", port),
		"healthEndpoint", fmt.Sprintf("http://localhost:%d/health", port),
	)

	// Create a mux to handle both MCP and health endpoints
	mux := http.NewServeMux()

	// Health endpoint for Kubernetes liveness/readiness probes
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// MCP endpoint handled by the Streamable HTTP handler
	streamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux.Handle("/mcp", streamHandler)
	mux.Handle("/", streamHandler)

	// Wrap with logging middleware
	handler := loggingMiddleware(mux, logger)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigChan

		logger.Info("Received shutdown signal", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("Error during shutdown", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server error", "error", err)
		os.Exit(1)
	}
	logger.Info("Server stopped")
}

// loggingMiddleware wraps an http.Handler with request logging and body size limits.
func loggingMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit request body size to 10MB to prevent DoS attacks
		r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		logger.Debug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"duration", time.Since(start),
			"remoteAddr", r.RemoteAddr,
		)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
// It implements http.Flusher to support SSE streaming.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher interface for SSE support.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
