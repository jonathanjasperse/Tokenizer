package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"tokenizer.local/internal/tokenizer"
)

//go:embed web
var webFiles embed.FS

//go:embed vocab/*.tiktoken
var vocabFiles embed.FS

const (
	maxRequestBytes = 1 << 20 // 1 MiB HTTP body
	maxTextBytes    = 100_000 // 100 KB of UTF-8 input
)

type app struct {
	registry *tokenizer.Registry
}

type tokenizeRequest struct {
	Text     string `json:"text"`
	Encoding string `json:"encoding"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	serve := flag.Bool("serve", false, "serve the UI over HTTP instead of opening the desktop window")
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address for server mode")
	debug := flag.Bool("debug", false, "enable WebView2 developer tools and context menus")
	dev := flag.Bool("dev", false, "serve UI files from ./web so edits appear on reload")
	flag.Parse()

	if *serve || !desktopDefault {
		runServer(*addr, *dev)
		return
	}

	if err := runDesktopApplication(*debug, *dev); err != nil {
		showErrorDialog("Tokenizer", err.Error())
		log.Printf("desktop app: %v", err)
		os.Exit(1)
	}
}

func newApplication(dev bool) (*app, http.Handler, error) {
	var webFS fs.FS
	var err error
	if dev {
		if _, statErr := os.Stat("web/index.html"); statErr != nil {
			return nil, nil, fmt.Errorf("development UI not found; start from the project folder containing web/index.html: %w", statErr)
		}
		webFS = os.DirFS("web")
	} else {
		webFS, err = fs.Sub(webFiles, "web")
		if err != nil {
			return nil, nil, fmt.Errorf("open embedded web assets: %w", err)
		}
	}
	vocabFS, err := fs.Sub(vocabFiles, "vocab")
	if err != nil {
		return nil, nil, fmt.Errorf("open embedded vocabularies: %w", err)
	}

	application := &app{registry: tokenizer.NewRegistry(vocabFS)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", application.handleHealth)
	mux.HandleFunc("GET /api/encodings", application.handleEncodings)
	mux.HandleFunc("POST /api/tokenize", application.handleTokenize)
	mux.Handle("GET /", http.FileServer(http.FS(webFS)))

	return application, recoverPanics(securityHeaders(logRequests(mux))), nil
}

func newHTTPServer(handler http.Handler, addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func runServer(addr string, dev bool) {
	application, handler, err := newApplication(dev)
	if err != nil {
		log.Fatal(err)
	}

	srv := newHTTPServer(handler, addr)

	if dev {
		log.Print("Development mode: serving UI from ./web")
	}
	log.Printf("Tokenizer listening on http://%s", addr)
	log.Printf("%d encodings available (loaded on first use)", len(application.registry.Infos()))

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		log.Fatalf("server error: %v", err)
	case <-ctx.Done():
	}

	log.Print("Shutting down…")
	shutdownServer(srv)
}

// startLoopbackServer serves the app on a private, random loopback port. The
// desktop window navigates to the returned URL.
func startLoopbackServer(handler http.Handler) (*http.Server, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("start internal server: %w", err)
	}

	srv := newHTTPServer(handler, "")
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("internal server: %v", err)
		}
	}()

	return srv, "http://" + listener.Addr().String() + "/", nil
}

func shutdownServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func (a *app) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleEncodings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"encodings": a.registry.Infos(),
	})
}

func (a *app) handleTokenize(w http.ResponseWriter, r *http.Request) {
	if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req tokenizeRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "Request body is too large.")
			return
		}
		writeError(w, http.StatusBadRequest, "Invalid JSON request body.")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "Request body must contain exactly one JSON object.")
		return
	}

	if req.Encoding == "" {
		writeError(w, http.StatusBadRequest, "The encoding field is required.")
		return
	}
	if len(req.Text) > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "Text is limited to 100,000 bytes in this demo.")
		return
	}

	encoding, err := a.registry.Get(req.Encoding)
	if err != nil {
		if errors.Is(err, tokenizer.ErrUnknownEncoding) {
			writeError(w, http.StatusBadRequest, "Unknown encoding: "+req.Encoding)
			return
		}
		log.Printf("load encoding %q: %v", req.Encoding, err)
		writeError(w, http.StatusInternalServerError, "Could not load the requested encoding.")
		return
	}

	result, err := encoding.Tokenize(req.Text)
	if err != nil {
		log.Printf("tokenize with %q: %v", req.Encoding, err)
		writeError(w, http.StatusInternalServerError, "Tokenization failed.")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		// Tokenizing happens on every keystroke (debounced); only log failures
		// for that endpoint to keep the console readable.
		if r.URL.Path == "/api/tokenize" && sw.status < http.StatusBadRequest {
			return
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}
