// Package httpserver exposes ruleengine.Engine over plain HTTP+JSON
// (FR1) and sets the standardized rate-limit headers adapters can
// forward to clients as-is (FR7).
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/aupv9/unigate/internal/ruleengine"
)

type Server struct {
	engine *ruleengine.Engine
	mux    *http.ServeMux
}

func New(engine *ruleengine.Engine) *Server {
	s := &Server{engine: engine, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/check", s.handleCheck)
	s.mux.HandleFunc("POST /v1/reset", s.handleReset)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type keyComponentDTO struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type checkRequestDTO struct {
	RuleID    string            `json:"rule_id"`
	Key       []keyComponentDTO `json:"key"`
	Cost      int64             `json:"cost"`
	Gateway   string            `json:"gateway"`
	Namespace string            `json:"namespace"`
}

type checkResponseDTO struct {
	Allow                   bool   `json:"allow"`
	Limit                   int64  `json:"limit"`
	Remaining               int64  `json:"remaining"`
	ResetSeconds            int64  `json:"reset_seconds"`
	RetryAfterSeconds       int64  `json:"retry_after_seconds,omitempty"`
	LockedOut               bool   `json:"locked_out"`
	LockoutRemainingSeconds int64  `json:"lockout_remaining_seconds,omitempty"`
	MatchedWindow           string `json:"matched_window,omitempty"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var dto checkRequestDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if dto.RuleID == "" {
		writeError(w, http.StatusBadRequest, errors.New("rule_id is required"))
		return
	}

	key := make([]ruleengine.KeyComponent, len(dto.Key))
	for i, k := range dto.Key {
		key[i] = ruleengine.KeyComponent{Kind: k.Kind, Value: k.Value}
	}

	res, err := s.engine.CheckLimit(r.Context(), ruleengine.CheckRequest{
		RuleID:    dto.RuleID,
		Key:       key,
		Cost:      dto.Cost,
		Gateway:   dto.Gateway,
		Namespace: dto.Namespace,
	})
	if err != nil {
		writeEngineError(w, err)
		return
	}

	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(res.Limit, 10))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(res.Remaining, 10))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(res.ResetSeconds, 10))
	if !res.Allow {
		retryAfter := res.RetryAfterSeconds
		if retryAfter <= 0 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	}

	status := http.StatusOK
	if !res.Allow {
		status = http.StatusTooManyRequests
	}
	writeJSON(w, status, checkResponseDTO{
		Allow:                   res.Allow,
		Limit:                   res.Limit,
		Remaining:               res.Remaining,
		ResetSeconds:            res.ResetSeconds,
		RetryAfterSeconds:       res.RetryAfterSeconds,
		LockedOut:               res.LockedOut,
		LockoutRemainingSeconds: res.LockoutRemainingSeconds,
		MatchedWindow:           res.MatchedWindow,
	})
}

type resetRequestDTO struct {
	RuleID    string            `json:"rule_id"`
	Key       []keyComponentDTO `json:"key"`
	Namespace string            `json:"namespace"`
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	var dto resetRequestDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	key := make([]ruleengine.KeyComponent, len(dto.Key))
	for i, k := range dto.Key {
		key[i] = ruleengine.KeyComponent{Kind: k.Kind, Value: k.Value}
	}
	if err := s.engine.Reset(r.Context(), ruleengine.ResetRequest{
		RuleID: dto.RuleID, Key: key, Namespace: dto.Namespace,
	}); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ruleengine.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ruleengine.ErrMissingKeyPart):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(w, `{"error":"encode response: %s"}`, err)
	}
}
