package napcat

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// authRejectedError marks an error as a credential rejection rather than a
// transport or business failure. A typed marker is what keeps IsAuthFailure
// narrow: the cached WebUI bridge may only be dropped (and a login spent) when
// the credential itself was refused.
type authRejectedError struct{ err error }

func (e *authRejectedError) Error() string { return e.err.Error() }
func (e *authRejectedError) Unwrap() error { return e.err }

func authRejected(err error) error {
	if err == nil {
		return nil
	}
	return &authRejectedError{err: err}
}

// IsAuthFailure reports whether err means a credential was refused and a fresh
// login is the cure.
//
// It is deliberately narrow. NapCat answers a missing/expired WebUI credential
// with sendError(res, 'Unauthorized'): an HTTP 200 whose envelope carries
// code -1 and the message "Unauthorized" (see the upstream middleware/auth.ts
// and utils/response.ts). A reverse proxy in front of NapCat may instead answer
// a bare 401/403. Everything else — a connection failure, NapCat's own login
// rate limit, a business failure such as an expired QQ login, or a 5xx — is
// reported as not-an-auth-failure, because re-logging-in cannot fix it and
// doing so spends the login rate limit the fallback depends on.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var rejected *authRejectedError
	if errors.As(err, &rejected) {
		return true
	}
	return false
}

// classifyWebUIError wraps the credential-rejection cases of a WebUI response.
func classifyWebUIError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		if httpErr.status == http.StatusUnauthorized || httpErr.status == http.StatusForbidden {
			return authRejected(err)
		}
		return err
	}
	if isUnauthorizedEnvelope(err) {
		return authRejected(err)
	}
	return err
}

// isUnauthorizedEnvelope recognizes NapCat's sendError('Unauthorized') shape.
// It matches the upstream wording, not a guess: any other message (including
// the login rate limit's "login rate limit", or "Server token not initialized",
// which a re-login cannot fix) stays unclassified.
func isUnauthorizedEnvelope(err error) bool {
	return strings.Contains(err.Error(), "Unauthorized")
}

// httpStatusError carries a response status so classification does not depend on
// the rendered message.
type httpStatusError struct {
	status int
	path   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.path, e.status)
}
