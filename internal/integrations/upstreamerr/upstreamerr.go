// Package upstreamerr renders errors of the Plex and *arr clients for messages that an API client
// can read (API error responses, health checks, scan runs) without the upstream's response body.
//
// A connection URL is admin-chosen (any host, any path prefix), so the body of an error answer may
// come from any internal service, not from Plex or an *arr: echoing it would let whoever can edit
// a connection read internal services' answers through Dupearr (SEC-031). Method, path and status
// are kept; a redirect keeps its (sanitized) target host.
package upstreamerr

import (
	"errors"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
)

// withheld is appended where a response body was removed.
const withheld = " (the server's response is not shown)"

// Message returns err's message with every response-body excerpt removed: the Message of an *arr
// HTTPError (except a redirect's) and the Body of a Plex StatusError, wherever they are in err's
// tree. Other errors are returned unchanged.
func Message(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	walk(err, func(e error) {
		switch he := e.(type) {
		case *arr.HTTPError:
			if he.Message != "" && !errors.Is(he.Err, arr.ErrRedirect) {
				stripped := *he
				stripped.Message = ""
				msg = strings.ReplaceAll(msg, he.Error(), stripped.Error()+withheld)
			}
		case *plex.StatusError:
			if he.Body != "" {
				stripped := *he
				stripped.Body = ""
				msg = strings.ReplaceAll(msg, he.Error(), stripped.Error()+withheld)
			}
		}
	})
	return msg
}

// walk calls fn for err and every error it wraps (Unwrap() error and Unwrap() []error).
func walk(err error, fn func(error)) {
	for depth := 0; err != nil && depth < 32; depth++ {
		fn(err)
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				walk(e, fn)
			}
			return
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return
		}
	}
}
