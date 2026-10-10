// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Exit codes (docs/spec/06-cli.md, section 1).
const (
	ExitOK               = 0
	ExitError            = 1
	ExitUsage            = 2
	ExitNotFound         = 3
	ExitPermission       = 4
	ExitDeployFailed     = 5
	ExitTimeout          = 6
	operationsAnnotation = "operations"
)

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return &exitError{ExitUsage, fmt.Sprintf(format, args...)}
}

func notFoundErr(format string, args ...any) error {
	return &exitError{ExitNotFound, fmt.Sprintf(format, args...)}
}

func failErr(code int, format string, args ...any) error {
	return &exitError{code, fmt.Sprintf(format, args...)}
}

// apiError is an error answer of the server.
type apiError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *apiError) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	if b.Len() == 0 {
		b.WriteString(http.StatusText(e.Status))
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request %s]", e.RequestID)
	}
	if e.Status == http.StatusUnauthorized {
		b.WriteString(". Run polipan login or set POLIPAN_TOKEN.")
	}
	return b.String()
}

func parseAPIError(status int, body []byte) error {
	e := &apiError{Status: status}
	var p struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &p) == nil {
		e.Code, e.Message, e.RequestID = p.Code, p.Message, p.RequestID
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("the server answered %d", status)
	}
	return e
}

// ExitCode maps an error to the exit code of the CLI.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ae *apiError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusUnauthorized, http.StatusForbidden:
			return ExitPermission
		}
		return ExitError
	}
	// Errors of cobra for a wrong command line.
	m := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "required flag", "invalid argument", "flag needs an argument", "accepts ", "requires "} {
		if strings.HasPrefix(m, p) {
			return ExitUsage
		}
	}
	return ExitError
}
