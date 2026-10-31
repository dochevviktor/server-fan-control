package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/ipmi"
)

// SystemFans has its own command worker: BMC calls never occupy the GPU worker.
type SystemFans interface {
	Snapshot() ipmi.Snapshot
	Config() ipmi.Settings
	Update(context.Context, ipmi.Settings, uint64) (ipmi.Settings, error)
	Set(context.Context, int, int) (ipmi.Result, error)
	Automatic(context.Context) (ipmi.Result, error)
}

func (s *Server) routeIPMI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/ipmi" && !strings.HasPrefix(r.URL.Path, "/api/v1/ipmi/") {
		return false
	}
	controller := s.options.IPMI
	if r.URL.Path == "/api/v1/ipmi" {
		if !onlyGET(w, r) {
			return true
		}
		if controller == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "state": "disabled", "reason": "Local IPMI is disabled; start with --ipmi to enable monitoring."})
		} else {
			writeJSON(w, http.StatusOK, controller.Snapshot())
		}
		return true
	}
	if controller == nil {
		writeError(w, http.StatusServiceUnavailable, "local IPMI is disabled")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.commandTimeout)
	defer cancel()
	switch r.URL.Path {
	case "/api/v1/ipmi/config":
		switch r.Method {
		case http.MethodGet:
			cfg := controller.Config()
			w.Header().Set("ETag", revisionETag(cfg.Revision))
			writeJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			match := r.Header.Get("If-Match")
			if match == "" {
				writeError(w, http.StatusPreconditionRequired, "If-Match with the current IPMI configuration ETag is required")
				return true
			}
			if len(match) < 3 || match[0] != '"' || match[len(match)-1] != '"' {
				writeError(w, http.StatusBadRequest, "If-Match must be a quoted revision")
				return true
			}
			expected, err := strconv.ParseUint(match[1:len(match)-1], 10, 64)
			if err != nil || revisionETag(expected) != match {
				writeError(w, http.StatusBadRequest, "If-Match must be a quoted revision")
				return true
			}
			var cfg ipmi.Settings
			if !decodeJSON(w, r, &cfg, 32<<10) {
				return true
			}
			if cfg.Revision != expected {
				writeError(w, http.StatusBadRequest, "configuration revision must match If-Match")
				return true
			}
			updated, err := controller.Update(ctx, cfg, expected)
			if err != nil {
				s.ipmiError(w, err)
				return true
			}
			w.Header().Set("ETag", revisionETag(updated.Revision))
			writeJSON(w, http.StatusOK, updated)
		default:
			methodNotAllowed(w, "GET, PUT")
		}
	case "/api/v1/ipmi/target":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		var input struct {
			Fan  int `json:"fan"`
			Duty int `json:"duty"`
		}
		if !decodeJSON(w, r, &input, 1024) {
			return true
		}
		if input.Fan < 1 || input.Fan > 3 || input.Duty < 1 || input.Duty > 100 {
			writeError(w, http.StatusBadRequest, "fan must be 1..3 and manual duty 1..100; use automatic restoration for owned fans")
			return true
		}
		result, err := controller.Set(ctx, input.Fan, input.Duty)
		if err != nil {
			s.ipmiError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
	case "/api/v1/ipmi/default":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		result, err := controller.Automatic(ctx)
		if err != nil {
			s.ipmiError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusNotFound, "endpoint not found")
	}
	return true
}
func (s *Server) ipmiError(w http.ResponseWriter, err error) {
	if errors.Is(err, config.ErrConflict) {
		w.Header().Set("ETag", revisionETag(s.options.IPMI.Config().Revision))
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if errors.Is(err, ipmi.ErrInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ipmi.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.serviceError(w, err)
}
