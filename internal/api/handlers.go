package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Handlers is the thin HTTP adapter over Service. Every method parses the
// request, calls a Service method, and translates the result + sentinel
// errors into JSON responses. No business logic lives here.
type Handlers struct {
	svc *Service
	log *slog.Logger
}

// NewHandlers constructs the HTTP adapter. A nil logger falls back to
// slog.Default so test setup stays one-liner.
func NewHandlers(svc *Service, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{svc: svc, log: log}
}

// Register wires Handlers methods onto the Go 1.22+ enhanced ServeMux. The
// method+path pattern keeps routing declarative without pulling in chi.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /calls", h.listCalls)
	mux.HandleFunc("GET /calls/{id}", h.getCall)
	mux.HandleFunc("GET /users", h.listUsers)
	mux.HandleFunc("GET /users/{upn}/calls", h.listUserCalls)
	mux.HandleFunc("GET /users/{upn}/health", h.getUserHealth)
	mux.HandleFunc("GET /mics/flaky", h.listFlakyMics)
	mux.HandleFunc("GET /network/hotspots", h.findNetworkHotspots)
	h.registerWriteRoutes(mux)
}

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Health(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) listCalls(w http.ResponseWriter, r *http.Request) {
	params, err := h.parseListCallsParams(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	calls, err := h.svc.ListCalls(r.Context(), params)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, calls)
}

func (h *Handlers) getCall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := h.svc.GetCall(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, detail)
}

func (h *Handlers) listUsers(w http.ResponseWriter, r *http.Request) {
	from, err := parseTimeQ(r, "from")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	users, err := h.svc.ListUsers(r.Context(), from, to)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, users)
}

func (h *Handlers) listUserCalls(w http.ResponseWriter, r *http.Request) {
	upn := r.PathValue("upn")
	from, err := parseTimeQ(r, "from")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	limit, err := parseIntQ(r, "limit", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	offset, err := parseIntQ(r, "offset", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	calls, err := h.svc.ListUserCalls(r.Context(), upn, from, to, limit, offset)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, calls)
}

func (h *Handlers) getUserHealth(w http.ResponseWriter, r *http.Request) {
	upn := r.PathValue("upn")
	from, err := parseTimeQ(r, "from")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	report, err := h.svc.BuildUserHealthReport(r.Context(), UserHealthParams{
		Upn:  upn,
		From: from,
		To:   to,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, report)
}

func (h *Handlers) listFlakyMics(w http.ResponseWriter, r *http.Request) {
	from, err := parseTimeQ(r, "from")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	minPct, err := parseFloatQ(r, "min_concealed_pct", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	minIncidents, err := parseIntQ(r, "min_incidents", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	limit, err := parseIntQ(r, "limit", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	mics, err := h.svc.FindFlakyMicrophones(r.Context(), FindFlakyMicParams{
		From:            from,
		To:              to,
		MinConcealedPct: minPct,
		MinIncidents:    minIncidents,
		Limit:           limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, mics)
}

// findNetworkHotspots is the Phase 4 read-only analytics endpoint. Same
// thin-adapter shape as listFlakyMics: parse query → Service → JSON. The
// method honours the `group_by` literal strings ("subnet" | "relay" |
// "subnet+relay"); unknown values are rejected by the Service layer as
// ErrBadRequest and surface here as a 400.
func (h *Handlers) findNetworkHotspots(w http.ResponseWriter, r *http.Request) {
	from, err := parseTimeQ(r, "from")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	minCalls, err := parseIntQ(r, "min_calls", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	minBadRatio, err := parseFloatQ(r, "min_bad_ratio", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	limit, err := parseIntQ(r, "limit", 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out, err := h.svc.FindNetworkHotspots(r.Context(), HotspotsParams{
		From:        from,
		To:          to,
		MinCalls:    minCalls,
		MinBadRatio: minBadRatio,
		GroupBy:     r.URL.Query().Get("group_by"),
		Limit:       limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// parseListCallsParams builds a ListCallsParams from query-string values.
// Any parse error (unparsable time/int) is returned as-is — those are
// already wrapped in ErrBadRequest by the helpers.
func (h *Handlers) parseListCallsParams(r *http.Request) (ListCallsParams, error) {
	from, err := parseTimeQ(r, "from")
	if err != nil {
		return ListCallsParams{}, err
	}
	to, err := parseTimeQ(r, "to")
	if err != nil {
		return ListCallsParams{}, err
	}
	limit, err := parseIntQ(r, "limit", 0)
	if err != nil {
		return ListCallsParams{}, err
	}
	offset, err := parseIntQ(r, "offset", 0)
	if err != nil {
		return ListCallsParams{}, err
	}
	q := r.URL.Query()
	return ListCallsParams{
		From:    from,
		To:      to,
		Verdict: q.Get("verdict"),
		Upn:     q.Get("upn"),
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// writeJSON serialises body as JSON with the supplied status. Marshalling
// errors are logged and surfaced as 500 — we do not attempt to rewrite
// headers once Write has been called.
func (h *Handlers) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Header already flushed; best we can do is log.
		h.log.Error("api: encode response", slog.String("err", err.Error()))
	}
}

// writeError maps sentinel errors to HTTP status codes and emits the flat
// {"error": "..."} shape. Unknown errors become 500 with a generic message —
// the real error is logged but never leaked to the client.
func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrBadRequest):
		h.log.Warn("api: bad request",
			slog.String("path", r.URL.Path),
			slog.String("err", err.Error()),
		)
		h.writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, ErrNotFound):
		h.writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	default:
		h.log.Error("api: internal error",
			slog.String("path", r.URL.Path),
			slog.String("err", err.Error()),
		)
		h.writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
