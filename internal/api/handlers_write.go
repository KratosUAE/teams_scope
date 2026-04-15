package api

// Write endpoints (POST/PUT/DELETE on /subnets, /usercards, ...) assume the
// server binds to 127.0.0.1 only. There is NO authentication: we rely on the
// docker-compose stack publishing the API port only on the loopback
// interface. If the listen address ever changes from localhost, every
// handler in this file MUST be gated behind auth or removed entirely. See
// task spec §constraint 4 (v1.2 analytics).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"teams_con/internal/store"
)

// maxJSONBodyBytes caps the size of inbound write bodies. These endpoints
// store small administrative records (subnet labels, user cards), never
// blobs — 64 KiB is two orders of magnitude more than any legitimate row
// and it shuts down trivially-large junk before we hit Mongo.
const maxJSONBodyBytes int64 = 64 << 10

// readJSONBody decodes r.Body into out, returning ErrBadRequest for
// malformed, oversized, or schema-violating payloads. DisallowUnknownFields
// is on so a typo in a JSON key is loud at the boundary instead of being
// silently dropped on the floor. Phase 2 (user cards) reuses this helper
// verbatim — keep it generic.
//
// w must be the handler's http.ResponseWriter so that http.MaxBytesReader can
// write 413 StatusRequestEntityTooLarge before returning the size error.
// Passing nil for w caused a nil-pointer panic inside the stdlib 413 path
// (C1 fix).
func readJSONBody(w http.ResponseWriter, r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %s", ErrBadRequest, err.Error())
	}
	return nil
}

// registerWriteRoutes wires every write-side handler onto mux. Called from
// Handlers.Register so the route table stays in one declarative place per
// concern (read vs write).
func (h *Handlers) registerWriteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /subnets", h.listSubnets)
	mux.HandleFunc("POST /subnets", h.upsertSubnet)
	mux.HandleFunc("PUT /subnets", h.upsertSubnet)
	// DELETE uses ?cidr=<canonical> rather than a path segment because Go
	// 1.22+ ServeMux treats `/` as a path separator and CIDR strings carry
	// a `/` from the mask (e.g. "10.0.0.0/24"). The query-string form
	// avoids the encoding trap entirely. This deviates from the original
	// spec's path-segment design — see design-subnets.md gotchas section.
	mux.HandleFunc("DELETE /subnets", h.deleteSubnet)

	// Usercards (Phase 2). {upn} is safe as a path segment — UPNs
	// contain '@' and '.' but never '/', so unlike subnet CIDRs we do
	// not need the query-string escape hatch.
	mux.HandleFunc("GET /usercards", h.listUserCards)
	mux.HandleFunc("GET /usercards/{upn}", h.getUserCard)
	mux.HandleFunc("PUT /usercards/{upn}", h.upsertUserCard)
	mux.HandleFunc("DELETE /usercards/{upn}", h.deleteUserCard)
}

// subnetUpsertRequest is the wire shape for POST/PUT /subnets. It mirrors
// UpsertSubnetParams 1:1 with json tags so curl users (and the cobra CLI)
// can hand-craft a body without consulting the Go source.
type subnetUpsertRequest struct {
	Cidr   string `json:"cidr"`
	Name   string `json:"name"`
	Office string `json:"office,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

func (h *Handlers) listSubnets(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ListSubnets(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Coerce nil → empty slice so the wire shape is always [] not null,
	// matching the existing read handlers' contract.
	if out == nil {
		out = []store.SubnetEntry{}
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) upsertSubnet(w http.ResponseWriter, r *http.Request) {
	// M2: reject any Content-Type that is not application/json so clients get
	// a clear 400 instead of a confusing decode error deep in readJSONBody.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		h.writeError(w, r, fmt.Errorf("%w: Content-Type must be application/json", ErrBadRequest))
		return
	}
	var req subnetUpsertRequest
	if err := readJSONBody(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	entry, err := h.svc.UpsertSubnet(r.Context(), UpsertSubnetParams{
		Cidr:   req.Cidr,
		Name:   req.Name,
		Office: req.Office,
		Kind:   req.Kind,
		Notes:  req.Notes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, entry)
}

func (h *Handlers) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	cidr := r.URL.Query().Get("cidr")
	if cidr == "" {
		h.writeError(w, r, fmt.Errorf("%w: missing cidr query param", ErrBadRequest))
		return
	}
	if err := h.svc.DeleteSubnet(r.Context(), cidr); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -- Usercards --------------------------------------------------------

// userCardUpsertRequest is the wire shape for PUT /usercards/{upn}. It
// mirrors UpsertUserCardParams 1:1 with json tags so curl users (and the
// cobra CLI) can hand-craft a body without consulting the Go source.
// The `upn` field is decoded from the body but overridden by the path
// segment inside upsertUserCard — the path always wins.
type userCardUpsertRequest struct {
	Upn         string   `json:"upn,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Location    string   `json:"location,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

func (h *Handlers) listUserCards(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ListUserCards(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Coerce nil → empty slice so the wire shape is always [] not null,
	// matching the rest of the read handlers.
	if out == nil {
		out = []store.UserCard{}
	}
	h.writeJSON(w, http.StatusOK, out)
}

// getUserCard honours REST convention: a missing card is 404 with the flat
// {"error":"not found"} body, even though the Service layer returns
// (nil, nil) for missing. This is the documented semantics difference —
// see Service.GetUserCard GoDoc.
func (h *Handlers) getUserCard(w http.ResponseWriter, r *http.Request) {
	upn := r.PathValue("upn")
	card, err := h.svc.GetUserCard(r.Context(), upn)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if card == nil {
		h.writeError(w, r, ErrNotFound)
		return
	}
	h.writeJSON(w, http.StatusOK, card)
}

func (h *Handlers) upsertUserCard(w http.ResponseWriter, r *http.Request) {
	// M2: reject any Content-Type that is not application/json so clients get
	// a clear 400 instead of a confusing decode error deep in readJSONBody.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		h.writeError(w, r, fmt.Errorf("%w: Content-Type must be application/json", ErrBadRequest))
		return
	}
	var req userCardUpsertRequest
	if err := readJSONBody(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	// Path always wins over body. This prevents a client from
	// accidentally creating a card for the wrong user by sending a
	// mismatched "upn" field in the JSON payload.
	upn := r.PathValue("upn")
	card, err := h.svc.UpsertUserCard(r.Context(), UpsertUserCardParams{
		Upn:         upn,
		DisplayName: req.DisplayName,
		Location:    req.Location,
		Tags:        req.Tags,
		Notes:       req.Notes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, card)
}

func (h *Handlers) deleteUserCard(w http.ResponseWriter, r *http.Request) {
	upn := r.PathValue("upn")
	if err := h.svc.DeleteUserCard(r.Context(), upn); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
