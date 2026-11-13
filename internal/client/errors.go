package client

import (
	"errors"
	"strings"
)

// ErrUnauthorized reports a token the API rejected. Callers surface this as a
// configuration problem rather than a transient failure.
var ErrUnauthorized = errors.New("unauthorized")

// ErrNotFound reports a scope or control the API does not know about.
var ErrNotFound = errors.New("not found")

// GraphQLError is one entry of a GraphQL errors array.
type GraphQLError struct {
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
}

// Errors is the set of errors returned by a single operation.
type Errors []GraphQLError

func (e Errors) Error() string {
	messages := make([]string, 0, len(e))
	for _, err := range e {
		messages = append(messages, err.Message)
	}
	return strings.Join(messages, "; ")
}
