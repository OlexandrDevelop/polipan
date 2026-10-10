// SPDX-License-Identifier: Apache-2.0

package api

import (
	_ "embed"
	"net/http"
)

// installAgent is the install script of a worker node (09 section 2, step 2). It holds no secret: the token and the
// CA fingerprint are options of the command line.
//
//go:embed install-agent.sh
var installAgent []byte

// installAgentScript serves /install-agent.sh. It is public on purpose: the command is `curl ... | sh`.
func installAgentScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(installAgent)
}
