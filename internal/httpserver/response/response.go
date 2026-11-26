package response

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"piped/internal/auth"
	"piped/internal/problem"
	"piped/internal/store"
)

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string              { return e.Message }
func Fail(status int, message string) error { return &Error{Status: status, Message: message} }

func PublicError(err error) *Error {
	var public *Error
	if failure, ok := problem.As(err); ok {
		status := map[problem.Kind]int{
			problem.Invalid:      400,
			problem.Unauthorized: 401,
			problem.Forbidden:    403,
			problem.Missing:      404,
			problem.Conflict:     409,
			problem.Unavailable:  503,
			problem.Uncertain:    503,
		}[failure.Kind()]
		if status != 0 {
			public = &Error{status, failure.Error()}
		}
	}
	switch {
	case public != nil:
	case errors.As(err, &public):
	case errors.Is(err, auth.ErrCredentials):
		public = &Error{401, "invalid username or password"}
	case errors.Is(err, auth.ErrPassword), errors.Is(err, auth.ErrUsername):
		public = &Error{400, err.Error()}
	case errors.Is(err, auth.ErrBusy):
		public = &Error{503, err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		public = &Error{503, "request timed out; try again"}
	case errors.Is(err, store.ErrConflict):
		public = &Error{409, "username already exists"}
	case errors.Is(err, store.ErrNotFound):
		public = &Error{404, "user not found"}
	case errors.Is(err, store.ErrUnauthorized):
		public = &Error{401, "session changed; sign in again"}
	case errors.Is(err, store.ErrForbidden):
		public = &Error{403, "API key does not permit this operation"}
	case errors.Is(err, auth.ErrAPIKeyInput):
		public = &Error{400, "use a name, valid scopes, and a supported expiry"}
	case errors.Is(err, store.ErrLastUser):
		public = &Error{409, "the last administrator cannot be removed"}
	default:
		public = &Error{500, "request failed; try again"}
	}
	return public
}

func WriteError(w http.ResponseWriter, err error, logger *slog.Logger) {
	public := PublicError(err)
	if public.Status == 500 {
		logger.Error("request failed", "error", err)
	}
	if public.Status == 503 {
		w.Header().Set("Retry-After", "2")
	}
	JSON(w, public.Status, map[string]string{"error": public.Message})
}

func Decode(w http.ResponseWriter, r *http.Request, target any) error {
	return DecodeLimit(w, r, target, 16*1024)
}

func DecodeLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return Fail(415, "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return Fail(400, "invalid JSON request")
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return Fail(400, "request must contain one JSON object")
	}
	return nil
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type Endpoint func(http.ResponseWriter, *http.Request, store.Actor) error

type Route struct {
	Method, Path, Scope string
	Handler             Endpoint
}
