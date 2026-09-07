// Package handler holds the HTTP handlers of the public API; they receive their dependencies
// explicitly and know nothing about configuration or metrics.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/logging"
)

// ProjectName is the fixed value of the "nome" field; accent and capitalization are part of the contract.
const ProjectName = "Projeto Korp"

// ContentTypeJSON is sent on every successful JSON response.
const ContentTypeJSON = "application/json; charset=utf-8"

// ProjetoKorpResponse is the JSON contract of GET /projeto-korp (REQ-05); the field names are
// fixed by the brief, so they stay in Portuguese.
type ProjetoKorpResponse struct {
	Nome    string `json:"nome" example:"Projeto Korp"`
	Horario string `json:"horario" example:"2026-09-07T12:00:00Z"`
}

// ProjetoKorp serves GET /projeto-korp. It has no mutable state and is safe for concurrent use.
type ProjetoKorp struct {
	now func() time.Time
	log *slog.Logger
}

// NewProjetoKorp builds the handler; now is injected so tests can prove the time is resolved per request.
func NewProjetoKorp(now func() time.Time, log *slog.Logger) *ProjetoKorp {
	return &ProjetoKorp{now: now, log: log}
}

// ServeHTTP resolves the clock inside the request, never at construction, so every call sees the
// current UTC time (REQ-06).
//
// @Summary      Project name and current UTC time
// @Description  Returns the project name and the current time in UTC (RFC 3339), resolved on every request.
// @Tags         projeto-korp
// @Produce      json
// @Success      200  {object}  ProjetoKorpResponse
// @Failure      405  {object}  problem.Problem  "Method not allowed (Allow: GET, HEAD)"
// @Header       200  {string}  X-Request-ID     "Correlation id, echoed from the request or generated"
// @Router       /projeto-korp [get]
func (h *ProjetoKorp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := ProjetoKorpResponse{
		Nome:    ProjectName,
		Horario: h.now().UTC().Format(time.RFC3339),
	}
	header := w.Header()
	header.Set("Content-Type", ContentTypeJSON)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// The 200 status already left with the first write; logging is the only action left.
		h.log.ErrorContext(r.Context(), "encode response", slog.Any(logging.KeyErr, err))
	}
}
