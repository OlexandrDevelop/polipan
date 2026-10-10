// SPDX-License-Identifier: Apache-2.0

// Command polipan is the command line client. It talks only to the public API
// through the generated client (REQ-CORE-009).
package main

import (
	"os"

	"github.com/polipan/polipan/cli/internal/commands"
)

func main() {
	os.Exit(commands.Execute(commands.Options{}, os.Args[1:]))
}
