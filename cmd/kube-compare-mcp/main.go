// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
	"github.com/openshift/kube-compare/pkg/rdsdiff"
)

var version = "dev"

func main() {
	// Parse command-line flags
	transport := flag.String("transport", "stdio", "Transport mode: stdio or http")
	port := flag.Int("port", 8080, "Port to listen on (for http transport)")
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
		handler = slog.NewJSONHandler(os.Stderr, opts)
	default:
		handler = slog.NewTextHandler(os.Stderr, opts)
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

	// Artifacts: serve RDS version diff session files at GET /artifacts/<session-id>/...
	workDir := os.Getenv("RDS_DIFF_WORK_DIR")
	if workDir == "" {
		workDir = os.TempDir()
	}
	mux.Handle("/artifacts/", artifactsHandler(workDir))

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

// artifactsHandler returns an http.Handler that serves session files under GET /artifacts/<session-id>/...
// workDir is the RDS_DIFF_WORK_DIR (or OS temp). Path traversal is prevented via rdsdiff.ResolveSessionPath.
func artifactsHandler(workDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/artifacts")
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			http.Error(w, "missing session id", http.StatusBadRequest)
			return
		}
		firstSlash := strings.Index(path, "/")
		var sessionID, subpath string
		if firstSlash < 0 {
			sessionID = path
			subpath = ""
		} else {
			sessionID = path[:firstSlash]
			subpath = path[firstSlash+1:]
		}
		sessionPath, err := rdsdiff.ResolveSessionPath(workDir, sessionID)
		if err != nil {
			if errors.Is(err, rdsdiff.ErrInvalidSessionID) {
				http.Error(w, "invalid session id", http.StatusBadRequest)
				return
			}
			if os.IsNotExist(err) {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Serve from sessionPath; request path becomes subpath (or "/" for directory listing)
		r2 := r.Clone(r.Context())
		r2.URL = cloneURL(r.URL)
		if subpath != "" {
			r2.URL.Path = "/" + subpath
		} else {
			r2.URL.Path = "/"
		}
		http.FileServer(http.Dir(sessionPath)).ServeHTTP(w, r2)
	})
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	u2 := *u
	return &u2
}

// loggingMiddleware wraps an http.Handler with request logging and body size limits.
func loggingMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit request body size to 10MB to prevent DoS attacks
		r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

		start := time.Now()

		// Log incoming MCP requests for observability
		if r.Method == "POST" && r.URL.Path == "/mcp" {
			logger.Info("Incoming MCP request",
				"contentLength", r.ContentLength,
				"remoteAddr", r.RemoteAddr,
			)
		}

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		// Skip logging for health checks to reduce log noise
		if r.URL.Path != "/health" {
			logger.Debug("HTTP request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.statusCode,
				"duration", time.Since(start),
				"remoteAddr", r.RemoteAddr,
			)
		}
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
// It implements http.Flusher to support HTTP streaming.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher interface for HTTP streaming support.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
