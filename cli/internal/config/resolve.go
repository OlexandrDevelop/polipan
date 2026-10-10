// SPDX-License-Identifier: Apache-2.0

package config

import "strings"

// Flags are the values of the global flags that can change the config.
type Flags struct {
	Context string
	URL     string
	Project string
	Env     string
}

// Effective is what the CLI uses for one run.
type Effective struct {
	ContextName string
	URL         string
	Token       string
	Project     string
	Env         string
}

// Resolve applies the order: flags, then env (POLIPAN_URL, POLIPAN_TOKEN, POLIPAN_CONTEXT, POLIPAN_PROJECT,
// POLIPAN_ENV), then the config file. kr may be nil.
func Resolve(f *File, kr Keyring, fl Flags, getenv func(string) string) Effective {
	var e Effective
	e.ContextName = first(fl.Context, getenv("POLIPAN_CONTEXT"), f.CurrentContext)
	ctx := f.Find(e.ContextName)
	if ctx == nil {
		ctx = &Context{}
	}
	e.URL = strings.TrimRight(first(fl.URL, getenv("POLIPAN_URL"), getenv("POLIPAN_SERVER"), ctx.URL), "/")
	e.Project = first(fl.Project, getenv("POLIPAN_PROJECT"), ctx.Project)
	e.Env = first(fl.Env, getenv("POLIPAN_ENV"), ctx.Env)
	e.Token = getenv("POLIPAN_TOKEN")
	if e.Token == "" {
		e.Token = ctx.Token
		if e.Token == "" && ctx.TokenInKeyring && kr != nil {
			if t, err := kr.Get(ctx.Name); err == nil {
				e.Token = strings.TrimSpace(t)
			}
		}
	}
	return e
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
