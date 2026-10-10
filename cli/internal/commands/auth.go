// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/cli/internal/apiops"
	"github.com/polipan/polipan/cli/internal/config"
)

func (a *app) authCommands() []*cobra.Command {
	return []*cobra.Command{
		a.loginCmd(), a.logoutCmd(), a.whoamiCmd(), a.contextCmd(), a.meCmd(), a.tokenCmd(),
		a.passwordCmd(), a.invitationCmd(), a.setupCmd(),
	}
}

func (a *app) loginCmd() *cobra.Command {
	var token, name, email, totp, expires string
	var tokenStdin, passwordStdin bool
	pw := &secretSource{label: "password"}
	c := a.cmd("login", "Log in to a server and save the context", "login,createToken,logout,getMe,listLoginProviders", noArgs, func(cmd *cobra.Command, _ []string) error {
		e, err := a.effective()
		if err != nil {
			return err
		}
		if e.URL == "" {
			return usageErr("no server set: use --url or POLIPAN_URL")
		}
		a.gc = nil
		switch {
		case token != "":
			a.info("warning: a token in the arguments can be seen by other users on this machine: prefer --token-stdin")
		case tokenStdin:
			b, err := readAll(a.readStdin())
			if err != nil {
				return err
			}
			token = strings.TrimSpace(b)
		case email != "":
			pw.stdin = passwordStdin
			t, err := a.passwordLogin(e.URL, email, pw, totp, expires)
			if err != nil {
				return err
			}
			token = t
		case a.interactive():
			a.info("This server has no browser login for the CLI. Create an API token in the web UI (Account, Tokens), or run: polipan login --email you@example.com")
			t, err := a.prompt("API token")
			if err != nil {
				return err
			}
			token = t
		default:
			return usageErr("no login method: use --token-stdin, or --email with --password-stdin. This server has no browser login for the CLI")
		}
		if token == "" {
			return usageErr("the token is empty")
		}
		a.gc = nil
		gc, err := gen.NewClient(e.URL+"/api/v1", gen.WithHTTPClient(a.httpClient(token)))
		if err != nil {
			return usageErr("the server address is not valid: %v", err)
		}
		a.gc = gc
		v, err := a.getJSON("getMe", ids())
		if err != nil {
			return err
		}
		ctxName := name
		if ctxName == "" {
			ctxName = e.ContextName
		}
		if ctxName == "" {
			ctxName = hostName(e.URL)
		}
		if err := a.saveContext(config.Context{Name: ctxName, URL: e.URL, Project: a.fl.project, Env: a.fl.env}, token); err != nil {
			return err
		}
		who := ""
		if m, ok := v.(map[string]any); ok {
			if u, ok := m["user"].(map[string]any); ok {
				who = str(u, "email")
			}
		}
		a.info("Logged in to %s as %s. Context %q is now the current context.", e.URL, who, ctxName)
		a.eff = nil
		return a.emit(map[string]any{"context": ctxName, "url": e.URL, "user": who})
	})
	c.Flags().StringVar(&token, "token", "", "API token (prefer --token-stdin: arguments can be seen by other users)")
	c.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from standard input")
	c.Flags().StringVar(&email, "email", "", "log in with email and password (the password comes from a prompt, a file or stdin)")
	c.Flags().StringVar(&pw.file, "password-file", "", "read the password from a file")
	c.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from standard input")
	c.Flags().StringVar(&totp, "totp-code", "", "one-time code when two-factor login is on")
	c.Flags().StringVar(&expires, "expires", "90d", "lifetime of the token made by --email login")
	c.Flags().StringVar(&name, "name", "", "name of the context (default: the host name)")
	return c
}

func hostName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "default"
	}
	return strings.ReplaceAll(u.Host, ":", "-")
}

func readAll(r interface{ ReadString(byte) (string, error) }) (string, error) {
	var b strings.Builder
	for {
		s, err := r.ReadString('\n')
		b.WriteString(s)
		if err != nil {
			break
		}
	}
	return b.String(), nil
}

// passwordLogin logs in with a session, makes an API token and ends the session.
func (a *app) passwordLogin(serverURL, email string, pw *secretSource, totp, expires string) (string, error) {
	password, err := a.readSecret(pw, true)
	if err != nil {
		return "", err
	}
	life, err := parseDuration(expires)
	if err != nil {
		return "", err
	}
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Transport: a.httpClient("").Transport}
	gc, err := gen.NewClient(serverURL+"/api/v1", gen.WithHTTPClient(hc))
	if err != nil {
		return "", usageErr("the server address is not valid: %v", err)
	}
	saved := a.gc
	a.gc = gc
	defer func() { a.gc = saved }()
	body := map[string]any{"email": email, "password": password}
	if totp != "" {
		body["totp_code"] = totp
	}
	r, err := a.call("login", apiops.Input{Body: jsonBody(body)})
	if err != nil {
		return "", err
	}
	hdr := http.Header{"X-Csrf-Token": []string{r.Header.Get("X-CSRF-Token")}}
	host, _ := os.Hostname()
	tokBody := map[string]any{"name": "polipan-cli on " + host, "scopes": []string{"read", "deploy", "write", "admin"}}
	if life > 0 {
		tokBody["expires_at"] = time.Now().Add(life).UTC().Format(time.RFC3339)
	}
	tr, err := a.call("createToken", apiops.Input{Body: jsonBody(tokBody), Header: hdr})
	if err != nil {
		return "", err
	}
	v, _ := decodeJSON(tr.Body)
	m, _ := v.(map[string]any)
	// End the browser session. The API token stays.
	_, _ = a.call("logout", apiops.Input{Header: hdr})
	return str(m, "token"), nil
}

// saveContext stores a context. The token goes to the keyring when there is one, else to the file (mode 0600).
func (a *app) saveContext(c config.Context, token string) error {
	f, err := a.config()
	if err != nil {
		return err
	}
	if kr := a.keyring(); kr != nil && kr.Set(c.Name, token) == nil {
		c.TokenInKeyring = true
	} else {
		c.Token = token
	}
	f.Upsert(c)
	f.CurrentContext = c.Name
	p, err := a.configPath()
	if err != nil {
		return err
	}
	return config.Save(p, f)
}

func (a *app) logoutCmd() *cobra.Command {
	return a.cmd("logout", "Remove the saved token of the current context", "", noArgs, func(cmd *cobra.Command, _ []string) error {
		f, err := a.config()
		if err != nil {
			return err
		}
		e, err := a.effective()
		if err != nil {
			return err
		}
		c := f.Find(e.ContextName)
		if c == nil {
			return a.done("not logged in")
		}
		if c.TokenInKeyring {
			if kr := a.keyring(); kr != nil {
				_ = kr.Delete(c.Name)
			}
		}
		c.Token, c.TokenInKeyring = "", false
		p, err := a.configPath()
		if err != nil {
			return err
		}
		if err := config.Save(p, f); err != nil {
			return err
		}
		return a.done("logged out of context " + c.Name)
	})
}

func (a *app) whoamiCmd() *cobra.Command {
	return a.cmd("whoami", "Show the current user and role", "getMe", noArgs, func(cmd *cobra.Command, _ []string) error {
		r, err := a.call("getMe", ids())
		if err != nil {
			return err
		}
		return a.emitBody(r, "EMAIL=user.email", "NAME=user.name", "ROLE=org_role", "ID=user.id", "TOTP=user.totp_enabled")
	})
}

func (a *app) contextCmd() *cobra.Command {
	ls := a.listCmd("ls", "List the contexts", "", noArgs, func(cmd *cobra.Command, _ []string) error {
		f, err := a.config()
		if err != nil {
			return err
		}
		e, _ := a.effective()
		rows := []any{}
		for _, c := range f.Contexts {
			rows = append(rows, map[string]any{"name": c.Name, "url": c.URL, "current": c.Name == e.ContextName, "project": c.Project, "env": c.Env})
		}
		return a.emit(rows, "name", "url", "current", "project", "env")
	})
	use := a.cmd("use <name>", "Make a context the current one", "", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		f, err := a.config()
		if err != nil {
			return err
		}
		if f.Find(args[0]) == nil {
			return notFoundErr("context %q not found", args[0])
		}
		f.CurrentContext = args[0]
		p, _ := a.configPath()
		if err := config.Save(p, f); err != nil {
			return err
		}
		return a.done("current context: " + args[0])
	})
	rm := a.cmd("rm <name>", "Remove a context", "", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		f, err := a.config()
		if err != nil {
			return err
		}
		c := f.Find(args[0])
		if c == nil {
			return notFoundErr("context %q not found", args[0])
		}
		if err := a.confirm("Remove context "+args[0], args[0]); err != nil {
			return err
		}
		if c.TokenInKeyring {
			if kr := a.keyring(); kr != nil {
				_ = kr.Delete(c.Name)
			}
		}
		f.Remove(args[0])
		p, _ := a.configPath()
		if err := config.Save(p, f); err != nil {
			return err
		}
		return a.done("removed context " + args[0])
	})
	return group("context", "List and switch server contexts", ls, use, rm)
}

func (a *app) meCmd() *cobra.Command {
	var name string
	var changePassword bool
	cur := &secretSource{label: "current password"}
	nw := &secretSource{label: "new password"}
	update := a.cmd("update", "Change your name or password", "updateMe", noArgs, func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		if name != "" {
			body["name"] = name
		}
		if changePassword {
			c, err := a.readSecret(cur, true)
			if err != nil {
				return err
			}
			n, err := a.readSecret(nw, true)
			if err != nil {
				return err
			}
			body["current_password"], body["new_password"] = c, n
		}
		if len(body) == 0 {
			return usageErr("nothing to change: use --name or --change-password")
		}
		r, err := a.call("updateMe", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	update.Flags().StringVar(&name, "name", "", "new display name")
	update.Flags().BoolVar(&changePassword, "change-password", false, "change the password")
	update.Flags().StringVar(&cur.file, "current-password-file", "", "read the current password from a file")
	update.Flags().StringVar(&nw.file, "new-password-file", "", "read the new password from a file")

	sessLs := a.listCmd("ls", "List your sessions", "listMySessions", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listMySessions", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "id", "ip", "user_agent", "created_at", "expires_at", "current")
	})
	sessRm := a.cmd("rm <session-id>", "Revoke one of your sessions", "revokeMySession", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if _, err := a.call("revokeMySession", ids(args[0])); err != nil {
			return err
		}
		return a.done("revoked session " + args[0])
	})

	enroll := a.cmd("enroll", "Start two-factor setup and print the otpauth address", "enrollTotp", noArgs, func(cmd *cobra.Command, _ []string) error {
		r, err := a.call("enrollTotp", ids())
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	verify := a.cmd("verify <code>", "Confirm two-factor setup with a code. Prints recovery codes once", "verifyTotp", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("verifyTotp", apiops.Input{Body: jsonBody(map[string]any{"totp_code": args[0]})})
		if err != nil {
			return err
		}
		a.info("Keep the recovery codes in a safe place. They are shown only once.")
		return a.emitBody(r)
	})
	dpw := &secretSource{label: "password"}
	disable := a.cmd("disable", "Turn two-factor off (asks for your password)", "disableTotp", noArgs, func(cmd *cobra.Command, _ []string) error {
		p, err := a.readSecret(dpw, true)
		if err != nil {
			return err
		}
		if _, err := a.call("disableTotp", apiops.Input{Body: jsonBody(map[string]any{"password": p})}); err != nil {
			return err
		}
		return a.done("two-factor is off")
	})
	disable.Flags().StringVar(&dpw.file, "password-file", "", "read the password from a file")
	return group("me", "Your own account", update, group("sessions", "Your sessions", sessLs, sessRm), group("totp", "Two-factor login", enroll, verify, disable))
}

func (a *app) tokenCmd() *cobra.Command {
	var scopes, expires string
	var all, anyProject bool
	create := a.cmd("create <name>", "Create an API token. The token is shown once", "createToken", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		sc := strings.Split(scopes, ",")
		body := map[string]any{"name": args[0], "scopes": sc}
		if a.fl.project != "" || cmd.Flags().Changed("project") {
			pid, err := a.projectID("")
			if err != nil {
				return err
			}
			body["project_id"] = pid
		}
		if expires != "" {
			d, err := parseDuration(expires)
			if err != nil {
				return err
			}
			body["expires_at"] = time.Now().Add(d).UTC().Format(time.RFC3339)
		}
		r, err := a.call("createToken", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		a.info("Copy the token now. It is not shown again.")
		return a.emitBody(r, "id", "name", "scopes", "expires_at", "token")
	})
	create.Flags().StringVar(&scopes, "scope", "read", "scopes, comma separated: read, deploy, write, admin")
	create.Flags().StringVar(&expires, "expires", "", "lifetime, for example 90d (default: no expiry)")
	ls := a.listCmd("ls", "List API tokens", "listTokens", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		if all {
			q.Set("all", "true")
		}
		items, err := a.list("listTokens", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "token_prefix", "scopes", "project_id", "expires_at", "last_used_at", "revoked_at")
	})
	ls.Flags().BoolVar(&all, "all", false, "list the tokens of all users (needs token:manage)")
	rm := a.cmd("rm <id>", "Revoke an API token", "revokeToken,revokeOrgToken", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Revoke token "+args[0], args[0]); err != nil {
			return err
		}
		op := "revokeToken"
		if anyProject {
			op = "revokeOrgToken"
		}
		if _, err := a.call(op, ids(args[0])); err != nil {
			return err
		}
		return a.done("revoked token " + args[0])
	})
	rm.Flags().BoolVar(&anyProject, "any", false, "revoke the token of another user (needs token:manage)")
	return group("token", "API tokens", create, ls, rm)
}

func (a *app) passwordCmd() *cobra.Command {
	request := a.cmd("request <email>", "Ask for a password reset email", "requestPasswordReset", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if _, err := a.call("requestPasswordReset", apiops.Input{Body: jsonBody(map[string]any{"email": args[0]})}); err != nil {
			return err
		}
		return a.done("if the address is known, a reset mail is on its way")
	})
	tok := &secretSource{label: "reset token"}
	pw := &secretSource{label: "new password"}
	confirm := a.cmd("confirm", "Set a new password with the token from the email", "confirmPasswordReset", noArgs, func(cmd *cobra.Command, _ []string) error {
		t, err := a.readSecret(tok, true)
		if err != nil {
			return err
		}
		p, err := a.readSecret(pw, true)
		if err != nil {
			return err
		}
		if _, err := a.call("confirmPasswordReset", apiops.Input{Body: jsonBody(map[string]any{"token": t, "password": p})}); err != nil {
			return err
		}
		return a.done("password changed")
	})
	confirm.Flags().StringVar(&tok.file, "token-file", "", "read the reset token from a file")
	confirm.Flags().StringVar(&pw.file, "password-file", "", "read the new password from a file")
	return group("password", "Password reset", group("reset", "Reset a forgotten password", request, confirm))
}

func (a *app) invitationCmd() *cobra.Command {
	var name string
	tok := &secretSource{label: "invitation token"}
	pw := &secretSource{label: "password"}
	accept := a.cmd("accept --name NAME", "Accept an invitation and set a password", "acceptInvitation", noArgs, func(cmd *cobra.Command, _ []string) error {
		if name == "" {
			return usageErr("--name is needed")
		}
		t, err := a.readSecret(tok, true)
		if err != nil {
			return err
		}
		p, err := a.readSecret(pw, true)
		if err != nil {
			return err
		}
		r, err := a.call("acceptInvitation", apiops.Input{Body: jsonBody(map[string]any{"token": t, "name": name, "password": p})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	accept.Flags().StringVar(&name, "name", "", "your display name")
	accept.Flags().StringVar(&tok.file, "token-file", "", "read the invitation token from a file")
	accept.Flags().StringVar(&pw.file, "password-file", "", "read the password from a file")
	return group("invitation", "Invitations", accept)
}

func (a *app) setupCmd() *cobra.Command {
	var email, name string
	tok := &secretSource{label: "setup token"}
	pw := &secretSource{label: "password"}
	first := a.cmd("first-user --email E --name N", "Create the first owner with the one-time setup token", "setupFirstUser", noArgs, func(cmd *cobra.Command, _ []string) error {
		if email == "" || name == "" {
			return usageErr("--email and --name are needed")
		}
		t, err := a.readSecret(tok, true)
		if err != nil {
			return err
		}
		p, err := a.readSecret(pw, true)
		if err != nil {
			return err
		}
		r, err := a.call("setupFirstUser", apiops.Input{Body: jsonBody(map[string]any{"setup_token": t, "email": email, "name": name, "password": p})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	first.Flags().StringVar(&email, "email", "", "email of the owner")
	first.Flags().StringVar(&name, "name", "", "name of the owner")
	first.Flags().StringVar(&tok.file, "setup-token-file", "", "read the setup token from a file")
	first.Flags().StringVar(&pw.file, "password-file", "", "read the password from a file")
	return group("setup", "First setup of a new server", first)
}
