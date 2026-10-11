// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var secretName = regexp.MustCompile(`(?i)(secret|token|signature|hmac|csrf|password|passwd|apikey|recovery)`)

// exprName gives the last name of an expression: a, x.a, x.a(), x[i] -> "a".
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.CallExpr:
		return exprName(v.Fun)
	case *ast.IndexExpr:
		return exprName(v.X)
	case *ast.StarExpr:
		return exprName(v.X)
	}
	return ""
}

// secretLike says if a name holds a secret value. ActorToken and similar are kinds of actor, not values.
func secretLike(name string) bool {
	return secretName.MatchString(name) && !strings.HasPrefix(name, "Actor")
}

func isConstant(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return v.Name == "nil" || v.Name == "true" || v.Name == "false"
	}
	return false
}

// REQ-SSO-006, 10-security section 1: a secret, a token or a signature is never compared with == or bytes.Equal,
// because that leaks where the first different byte is. The code uses crypto/subtle.ConstantTimeCompare or
// hmac.Equal. The test reads the source of the server and fails on a comparison of two values when one of them has a
// name like secret, token, signature, hmac, csrf or password. Lookups by the hash of a token in the database are
// not comparisons in Go. A line that is safe and has such a name goes on the allow list with the reason.
func TestREQ_SSO_006_SecretsAreComparedInConstantTime(t *testing.T) {
	allowed := map[string]string{}
	fset := token.NewFileSet()
	var found []string
	files := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.Contains(path, "/gen/") || strings.Contains(path, "test/") || strings.Contains(path, "ssotest") {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../")
		if _, ok := allowed[rel]; ok {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files++
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				if (v.Op == token.EQL || v.Op == token.NEQ) && !isConstant(v.X) && !isConstant(v.Y) &&
					(secretLike(exprName(v.X)) || secretLike(exprName(v.Y))) {
					found = append(found, fset.Position(v.Pos()).String())
				}
			case *ast.CallExpr:
				fn := exprName(v.Fun)
				if (fn == "Equal" || fn == "EqualFold") && len(v.Args) == 2 {
					if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && (pkg.Name == "bytes" || pkg.Name == "strings") &&
							(secretLike(exprName(v.Args[0])) || secretLike(exprName(v.Args[1]))) {
							found = append(found, fset.Position(v.Pos()).String())
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("only %d files were read: the walk is wrong", files)
	}
	if len(found) > 0 {
		t.Fatalf("a secret, token or signature is compared without a constant-time function:\n%s", strings.Join(found, "\n"))
	}
}
