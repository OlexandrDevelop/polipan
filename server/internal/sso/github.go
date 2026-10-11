// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// GitHubScopes are the scopes of the GitHub preset (10-security, section 2).
var GitHubScopes = []string{"read:user", "user:email", "read:org"}

func (c Config) finishGitHub(ctx context.Context, code, verifier string) (*Identity, error) {
	tok, err := c.exchange(ctx, orDefault(c.GitHubTokenURL, "https://github.com/login/oauth/access_token"), code, verifier)
	if err != nil {
		return nil, err
	}
	api := strings.TrimRight(orDefault(c.GitHubAPIURL, "https://api.github.com"), "/")
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.getJSON(ctx, api+"/user", tok.AccessToken, &user); err != nil {
		return nil, err
	}
	if user.ID == 0 {
		return nil, errors.New("sso: GitHub sent no user id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.getJSON(ctx, api+"/user/emails", tok.AccessToken, &emails); err != nil {
		return nil, err
	}
	id := &Identity{Subject: strconv.FormatInt(user.ID, 10), Name: orDefault(user.Name, user.Login)}
	// Only the primary email counts, and only when GitHub checked it.
	for _, e := range emails {
		if e.Primary && e.Verified {
			id.Email, id.EmailVerified = strings.ToLower(strings.TrimSpace(e.Email)), true
		}
	}
	for page := 1; page <= 10; page++ {
		var teams []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		if err := c.getJSON(ctx, fmt.Sprintf("%s/user/teams?per_page=100&page=%d", api, page), tok.AccessToken, &teams); err != nil {
			return nil, err
		}
		for _, t := range teams {
			id.Groups = append(id.Groups, strings.ToLower(t.Organization.Login+"/"+t.Slug))
		}
		if len(teams) < 100 {
			break
		}
	}
	return id, nil
}
