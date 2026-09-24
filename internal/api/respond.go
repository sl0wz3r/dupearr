package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	// maskedSecret replaces secrets in responses; sending it back keeps the stored value.
	maskedSecret = "********"
	// maxJSONBody caps request bodies (except the backup upload).
	maxJSONBody = 1 << 20

	defaultPageSize = 20
	maxPageSize     = 1000
)

// apiError is an error with an HTTP status and a client-safe message (or validation failures).
type apiError struct {
	status int
	msg    string
	errs   []config.ValidationError
	cause  error
}

func (e *apiError) Error() string {
	if e.cause != nil {
		return e.msg + ": " + e.cause.Error()
	}
	return e.msg
}

func (e *apiError) Unwrap() error { return e.cause }

func errStatus(status int, format string, args ...any) *apiError {
	return &apiError{status: status, msg: fmt.Sprintf(format, args...)}
}

func errBadRequest(format string, args ...any) *apiError {
	return errStatus(http.StatusBadRequest, format, args...)
}

func errNotFound(what string) *apiError {
	return errStatus(http.StatusNotFound, "%s not found", what)
}

func errConflict(format string, args ...any) *apiError {
	return errStatus(http.StatusConflict, format, args...)
}

func errUnavailable(what string) *apiError {
	return errStatus(http.StatusServiceUnavailable, "%s is not available", what)
}

// errValidation returns a 400 with the given failures.
func errValidation(errs ...config.ValidationError) *apiError {
	return &apiError{status: http.StatusBadRequest, msg: "Validation failed", errs: errs}
}

func invalid(prop, format string, args ...any) config.ValidationError {
	return config.ValidationError{PropertyName: prop, ErrorMessage: fmt.Sprintf(format, args...)}
}

// notFoundAs turns store.ErrNotFound into a 404 naming what; other errors are returned as is.
func notFoundAs(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound(what)
	}
	return err
}

// writeJSON encodes v (nil slices/maps become []/{}) with the given status.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(nonNil(v))
	if err != nil {
		s.log.Error("Failed to encode a response", "error", err)
		writeMessage(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// writeMessage writes {"message": msg}.
func writeMessage(w http.ResponseWriter, status int, msg string) {
	body, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{msg})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// writeValidation writes 400 [{propertyName, errorMessage}].
func (s *Server) writeValidation(w http.ResponseWriter, errs []config.ValidationError) {
	if errs == nil {
		errs = []config.ValidationError{}
	}
	s.writeJSON(w, http.StatusBadRequest, errs)
}

// empty is the {} response body.
var empty = struct{}{}

// statusClientClosed is logged (never really sent) when the client went away mid-request.
const statusClientClosed = 499

// errorResponse maps err to an HTTP status and a client-safe message, or to validation failures
// (verrs != nil ⇒ 400 array). Unexpected errors are logged and reported as a generic 500, so
// internal error text never reaches the client. writeErr and the bulk endpoints share it.
func (s *Server) errorResponse(r *http.Request, err error) (status int, msg string, verrs []config.ValidationError) {
	var (
		ae *apiError
		ve config.ValidationErrors
	)
	switch {
	case errors.As(err, &ae):
		if ae.errs != nil {
			return http.StatusBadRequest, ae.msg, ae.errs
		}
		if ae.status >= 500 && ae.cause != nil {
			s.log.Error(ae.msg, "method", r.Method, "path", r.URL.Path, "error", ae.cause)
		}
		return ae.status, ae.msg, nil
	case errors.As(err, &ve):
		if ve == nil {
			ve = config.ValidationErrors{}
		}
		return http.StatusBadRequest, "Validation failed", ve
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "Not found", nil
	case errors.Is(err, database.ErrDefaultProfile):
		return http.StatusConflict, "The default profile cannot be deleted; make another profile the default first", nil
	case errors.Is(err, executor.ErrNotApprovable):
		// A status change raced the request (e.g. a scan queued/resolved the group meanwhile).
		msg := "This duplicate cannot be approved"
		if reason := reasonAfter(err, executor.ErrNotApprovable); reason != "" {
			msg += ": " + truncate(reason, maxMessageLen)
		}
		return http.StatusConflict, msg, nil
	case errors.Is(err, database.ErrActionNotPending), errors.Is(err, database.ErrNotRemovable):
		return http.StatusConflict, "The action is no longer pending or its file is no longer marked for removal", nil
	case errors.Is(err, database.ErrConstraint):
		return http.StatusConflict, "The change conflicts with existing data (duplicate or referenced item)", nil
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		return statusClientClosed, "Request cancelled", nil
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("Request timed out", "method", r.Method, "path", r.URL.Path, "error", err)
		return http.StatusGatewayTimeout, "The operation timed out", nil
	}
	s.log.Error("Request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	return http.StatusInternalServerError, "Internal server error", nil
}

// reasonAfter returns the part of err's message that follows sentinel's text (the detail a
// wrapping fmt.Errorf("%w: detail") added), or "".
func reasonAfter(err, sentinel error) string {
	msg, s := err.Error(), sentinel.Error()
	i := strings.Index(msg, s)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(msg[i+len(s):], ":"))
}

// writeErr maps an error to a response (see errorResponse); nil writes 200 {}.
func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		s.writeJSON(w, http.StatusOK, empty)
		return
	}
	status, msg, verrs := s.errorResponse(r, err)
	switch {
	case verrs != nil:
		s.writeValidation(w, verrs)
	case status == statusClientClosed:
		// The client went away; nothing useful can be written.
		w.WriteHeader(statusClientClosed)
	default:
		writeMessage(w, status, msg)
	}
}

// decodeJSON decodes the request body into dst (max 1 MiB). dst may be pre-filled: fields absent
// from the body keep their values (only use that for flat structs; encoding/json reuses existing
// slice elements).
//
// A non-empty body must be declared as JSON (Content-Type application/json or */*+json, any
// charset), else 415: a cross-site HTML form can only send form or text/plain bodies without a
// CORS preflight, so a JSON-only API cannot be driven by one even where a credential rides along.
func decodeJSON(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, maxJSONBody)
	defer body.Close()
	br := bufio.NewReader(body)
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		if _, err := br.Peek(1); err != nil {
			return errBadRequest("Request body can't be empty")
		}
		return errStatus(http.StatusUnsupportedMediaType, "Send the request body as JSON (Content-Type: application/json)")
	}
	dec := json.NewDecoder(br)
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return errBadRequest("Request body can't be empty")
		case errors.As(err, &mbe):
			return errStatus(http.StatusRequestEntityTooLarge, "Request body is too large")
		default:
			return errBadRequest("Invalid JSON body: %s", jsonProblem(err))
		}
	}
	if dec.More() {
		return errBadRequest("Invalid JSON body: unexpected data after the JSON value")
	}
	return nil
}

// isJSONContentType reports whether a Content-Type header declares JSON.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || (strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json"))
}

// jsonProblem describes a decoding error without echoing large parts of the input.
func jsonProblem(err error) string {
	var (
		se *json.SyntaxError
		te *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("syntax error at offset %d", se.Offset)
	case errors.As(err, &te):
		if te.Field != "" {
			return fmt.Sprintf("%q must be a %s", te.Field, jsonTypeName(te.Type.Kind().String()))
		}
		return fmt.Sprintf("expected a %s", jsonTypeName(te.Type.Kind().String()))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of input"
	}
	return "malformed JSON"
}

func jsonTypeName(kind string) string {
	switch kind {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		return "number"
	case "slice", "array":
		return "list"
	case "struct", "map":
		return "object"
	case "bool":
		return "boolean"
	}
	return kind
}

// pathID parses the {name} path value as a positive id.
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errBadRequest("%q is not a valid ID", truncate(raw, 40))
	}
	return id, nil
}

// queryInt parses an optional non-negative integer query parameter (0 when absent).
func queryInt(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errValidation(invalid(name, "Must be a non-negative integer"))
	}
	return n, nil
}

// queryList returns a list query parameter, accepting comma lists and repeated keys
// (status=a,b&status=c). Empty items are dropped.
func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// parsePaging reads page, pageSize, sortKey and sortDirection (docs/API.md "Paging"): page ≥ 1,
// 1 ≤ pageSize ≤ 1000 (default 20). Unknown sort keys are left to the repository whitelist.
func parsePaging(r *http.Request, defaultSortKey, defaultDirection string) (store.Paging, error) {
	q := r.URL.Query()
	p := store.Paging{Page: 1, PageSize: defaultPageSize, SortKey: defaultSortKey, SortDirection: defaultDirection}
	var errs []config.ValidationError
	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			errs = append(errs, invalid("page", "Must be an integer"))
		} else {
			p.Page = max(n, 1)
		}
	}
	if raw := strings.TrimSpace(q.Get("pageSize")); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, invalid("pageSize", "Must be an integer"))
		case n < 1:
			p.PageSize = defaultPageSize
		default:
			p.PageSize = min(n, maxPageSize)
		}
	}
	if k := strings.TrimSpace(q.Get("sortKey")); k != "" {
		p.SortKey = truncate(k, 64)
	}
	switch d := strings.ToLower(strings.TrimSpace(q.Get("sortDirection"))); d {
	case "ascending", "descending":
		p.SortDirection = d
	case "", "default":
	default:
		errs = append(errs, invalid("sortDirection", "Must be ascending or descending"))
	}
	if len(errs) > 0 {
		return p, errValidation(errs...)
	}
	return p, nil
}

// fsNotFound reports whether err means "no such file or item".
func fsNotFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, store.ErrNotFound)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
