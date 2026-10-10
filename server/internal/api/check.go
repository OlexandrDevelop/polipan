// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Special x-permission values that are not in the permission matrix.
const (
	PermPublic        = "public"
	PermAuthenticated = "authenticated"
)

// Route is a method and a path pattern that the router serves.
type Route struct {
	Method  string
	Pattern string
}

// nonOperationRoutes are routes under the base path that are not operations
// of the OpenAPI file (REQ-API-100: the file itself).
var nonOperationRoutes = map[Route]bool{
	{Method: "GET", Pattern: BasePath + "/openapi.yaml"}: true,
}

var paramRe = regexp.MustCompile(`\{[^}]*\}`)

// normalize makes /a/{id} and /a/{name} equal.
func normalize(path string) string { return paramRe.ReplaceAllString(path, "{}") }

// CheckPermissions checks REQ-API-101 (first part): every operation has an
// operationId and an x-permission. When allowed is not nil, the value must be
// public, authenticated or in allowed (the matrix of 10-security).
func CheckPermissions(ops []Operation, allowed map[string]bool) error {
	var problems []string
	seen := map[string]bool{}
	for _, o := range ops {
		where := o.Method + " " + o.Path
		if o.OperationID == "" {
			problems = append(problems, where+": operationId is missing")
		} else if seen[o.OperationID] {
			problems = append(problems, where+": operationId "+o.OperationID+" is used twice")
		}
		seen[o.OperationID] = true
		switch {
		case o.Permission == "":
			problems = append(problems, where+": x-permission is missing")
		case allowed != nil && o.Permission != PermPublic && o.Permission != PermAuthenticated && !allowed[o.Permission]:
			problems = append(problems, fmt.Sprintf("%s: x-permission %q is not in the permission matrix", where, o.Permission))
		}
	}
	return joinProblems("openapi check", problems)
}

// CheckRoutes checks REQ-API-101 (second part): every route under the base path
// is an operation of the OpenAPI file, and every operation has a route.
func CheckRoutes(ops []Operation, routes []Route) error {
	var problems []string
	inSpec := map[Route]bool{}
	for _, o := range ops {
		inSpec[Route{o.Method, normalize(o.FullPath())}] = true
	}
	inRouter := map[Route]bool{}
	for _, r := range routes {
		if !strings.HasPrefix(r.Pattern, BasePath+"/") && r.Pattern != BasePath {
			continue
		}
		if nonOperationRoutes[r] {
			continue
		}
		key := Route{r.Method, normalize(r.Pattern)}
		inRouter[key] = true
		if !inSpec[key] {
			problems = append(problems, fmt.Sprintf("route %s %s is not in the OpenAPI file", r.Method, r.Pattern))
		}
	}
	for _, o := range ops {
		if !inRouter[Route{o.Method, normalize(o.FullPath())}] {
			problems = append(problems, fmt.Sprintf("operation %s %s (%s) has no route", o.Method, o.FullPath(), o.OperationID))
		}
	}
	return joinProblems("route check", problems)
}

func joinProblems(title string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	errs := make([]error, len(problems))
	for i, p := range problems {
		errs[i] = errors.New(p)
	}
	return fmt.Errorf("%s failed: %w", title, errors.Join(errs...))
}
