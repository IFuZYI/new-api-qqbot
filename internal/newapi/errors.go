package newapi

import (
	"errors"
	"fmt"
)

// AmbiguousWriteError means a non-idempotent write may already have taken effect.
// Retrying it automatically can pay twice; callers must retain the pending claim.
type AmbiguousWriteError struct{ Err error }

func (e *AmbiguousWriteError) Error() string {
	return "gateway write outcome unknown: " + e.Err.Error()
}
func (e *AmbiguousWriteError) Unwrap() error        { return e.Err }
func (e *AmbiguousWriteError) AmbiguousWrite() bool { return true }

// IsAmbiguousWrite recognizes wrapped uncertain outcomes, not definite refusals.
func IsAmbiguousWrite(err error) bool {
	var uncertain *AmbiguousWriteError
	return errors.As(err, &uncertain)
}

// HTTPError preserves the status and machine code even without a JSON envelope.
// Message is diagnostic only; classification should use StatusCode and Code.
type HTTPError struct {
	Method, Path  string
	StatusCode    int
	Code, Message string
}

func (e *HTTPError) Error() string {
	msg := e.Message
	if e.Code != "" {
		msg = e.Code + ": " + msg
	}
	return fmt.Sprintf("%s %s: %s (HTTP %d)", e.Method, e.Path, msg, e.StatusCode)
}
