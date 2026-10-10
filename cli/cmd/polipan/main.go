// SPDX-License-Identifier: Apache-2.0

// Command polipan is the command line client. It talks only to the public API
// through the generated client (REQ-CORE-009). This is the M0 skeleton with the version command.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/cli/internal/version"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "polipan:", err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	var server string
	root := &cobra.Command{
		Use:           "polipan",
		Short:         "Command line client of Polipan",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&server, "server", os.Getenv("POLIPAN_SERVER"), "address of the server, for example https://panel.example.com")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the client version, and the server version when --server is set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "polipan %s (%s)\n", version.Version, version.Commit); err != nil {
				return err
			}
			if server == "" {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			return printServerVersion(ctx, out, server)
		},
	})
	return root
}

func printServerVersion(ctx context.Context, out io.Writer, server string) error {
	client, err := gen.NewClient(strings.TrimRight(server, "/") + "/api/v1")
	if err != nil {
		return err
	}
	resp, err := client.GetVersion(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server answered %d", resp.StatusCode)
	}
	var v struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		API     string `json:"api"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "server %s (%s), api %s\n", v.Version, v.Commit, v.API)
	return err
}
