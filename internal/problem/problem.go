// Package problem defines application failures independently of any transport.
package problem

import "errors"

type Kind string

const (
	Invalid      Kind = "invalid"
	Unauthorized Kind = "unauthorized"
	Forbidden    Kind = "forbidden"
	Missing      Kind = "missing"
	Conflict     Kind = "conflict"
	Unavailable  Kind = "unavailable"
	Uncertain    Kind = "uncertain_commit"
)

type Error struct {
	kind    Kind
	message string
}

func New(kind Kind, message string) *Error { return &Error{kind: kind, message: message} }
func (e *Error) Error() string             { return e.message }
func (e *Error) Kind() Kind                { return e.kind }

type Failure interface {
	error
	Kind() Kind
}

func As(err error) (Failure, bool) {
	var failure Failure
	ok := errors.As(err, &failure)
	return failure, ok
}
