// SPDX-License-Identifier: Apache-2.0

package api

import (
	"html/template"
	"net/http"
)

var docsTemplate = template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Polipan API</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 70rem; padding: 0 1rem; line-height: 1.5; }
table { border-collapse: collapse; width: 100%; }
th, td { border-bottom: 1px solid #d6dae1; padding: .4rem .6rem; text-align: left; vertical-align: top; }
code { font-family: ui-monospace, monospace; }
</style>
</head>
<body>
<h1>Polipan API</h1>
<p>The file <a href="/api/v1/openapi.yaml">/api/v1/openapi.yaml</a> is the source of truth. This page works offline.</p>
<table>
<thead><tr><th>Method</th><th>Path</th><th>Operation</th><th>Permission</th><th>Summary</th></tr></thead>
<tbody>
{{range .}}<tr><td><code>{{.Method}}</code></td><td><code>{{.FullPath}}</code></td><td>{{.OperationID}}</td><td><code>{{.Permission}}</code></td><td>{{.Summary}}</td></tr>
{{end}}</tbody>
</table>
</body>
</html>
`))

// docsHandler renders a simple page with all operations (REQ-API-100).
func docsHandler(ops []Operation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = docsTemplate.Execute(w, ops)
	})
}
