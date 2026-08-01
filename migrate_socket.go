package goql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aekis-dev/goql/models"
)

// ErrSocketConfig means the migrate socket was asked for without the configuration it
// requires.
var ErrSocketConfig = errors.New("migrate socket misconfigured")

// MigrateSocket serves the migration flow to an external CLI over a Unix domain socket.
//
// Why a socket at all: resolving a model's fields needs the models registered, which only
// happens inside the process that imported them. So the diff engine lives here, and the
// interactive prompting lives in a separate program that talks to it.
//
// It is deliberately awkward to switch on. The socket can apply DDL, which makes it a
// control channel into a running application:
//   - off unless explicitly enabled;
//   - a token is required and has no default;
//   - Unix domain only, created 0600, so filesystem permissions gate access;
//   - enabling it logs loudly.
type MigrateSocket struct {
	engine   *Engine
	entities []models.Entity
	token    string
	path     string
	listener net.Listener
	server   *http.Server
}

// ApplyResponse is what the socket returns from an apply attempt. Error is empty on
// success; Summary is populated either way, since a non-transactional engine may have
// applied part of the migration before failing.
type ApplyResponse struct {
	Error   string   `json:"error,omitempty"`
	Summary *Summary `json:"summary"`
}

// MigrateSocketConfig configures the socket. Both fields are required.
type MigrateSocketConfig struct {
	// Path is where the Unix socket is created. An existing file is replaced.
	Path string

	// Token must be presented by the client in the X-Goql-Token header. There is no
	// default: a socket that can change your schema should not be reachable by accident.
	Token string
}

// NewMigrateSocket prepares a socket for the given models. Call Serve to accept clients.
func (ctx *Engine) NewMigrateSocket(entities []models.Entity, cfg MigrateSocketConfig) (*MigrateSocket, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("%w: Path is required", ErrSocketConfig)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("%w: Token is required and has no default", ErrSocketConfig)
	}
	if len(entities) == 0 {
		return nil, fmt.Errorf("%w: no models supplied", ErrSocketConfig)
	}

	return &MigrateSocket{
		engine:   ctx,
		entities: entities,
		token:    cfg.Token,
		path:     cfg.Path,
	}, nil
}

// Serve starts accepting clients and blocks until Close is called.
func (ms *MigrateSocket) Serve() error {
	if err := os.Remove(ms.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if dir := filepath.Dir(ms.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	listener, err := net.Listen("unix", ms.path)
	if err != nil {
		return err
	}
	// Only the owner may talk to a socket that can alter the schema.
	if err := os.Chmod(ms.path, 0o600); err != nil {
		listener.Close()
		return err
	}
	ms.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/plan", ms.authed(ms.handlePlan))
	mux.HandleFunc("/apply", ms.authed(ms.handleApply))
	ms.server = &http.Server{Handler: mux}

	log.Printf("goql: migrate socket listening on %s — it can ALTER this database's schema; "+
		"remove it when you are done", ms.path)

	err = ms.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops serving and removes the socket file. It is safe to call more than once.
func (ms *MigrateSocket) Close() error {
	if ms.server != nil {
		if err := ms.server.Close(); err != nil {
			return err
		}
	}
	// Closing the listener already unlinks a Unix socket, so a missing file here is the
	// normal case rather than a failure.
	if err := os.Remove(ms.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path reports where the socket was created.
func (ms *MigrateSocket) Path() string { return ms.path }

// authed rejects a request that does not present the configured token.
func (ms *MigrateSocket) authed(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goql-Token") != ms.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (ms *MigrateSocket) handlePlan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Decisions map[string]string `json:"decisions"`
	}
	// A plan request may carry decisions already gathered, so the client can refine.
	_ = json.NewDecoder(r.Body).Decode(&request)

	plan, err := ms.engine.MigrationPlan(r.Context(), ms.entities, request.Decisions)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, plan)
}

func (ms *MigrateSocket) handleApply(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Decisions map[string]string `json:"decisions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, err)
		return
	}

	summary, err := ms.engine.Migrate(r.Context(), ms.entities, request.Decisions)

	// Always the same envelope: on failure the summary still describes how far a
	// non-transactional engine got before stopping.
	response := ApplyResponse{Summary: summary}
	if err != nil {
		response.Error = err.Error()
	}
	writeJSON(w, response)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// DialMigrateSocket returns an HTTP client that reaches a migrate socket, for building a
// migration CLI.
func DialMigrateSocket(path string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		},
	}
}
