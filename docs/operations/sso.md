# Single sign-on (SSO)

People can sign in with Google, GitHub or any OpenID Connect (OIDC) provider. You set it up in
**Settings → SSO** (needs the role admin or owner). The same is possible with the CLI (`polipan sso ...`) and the API
(`/sso-providers`).

## Add a provider

1. In the provider (Google Cloud console, GitHub *Developer settings → OAuth Apps*, Keycloak, Authentik, Okta, ...)
   make an OAuth client. As the **redirect address** use the one the SSO page shows:
   `https://<your panel>/api/v1/auth/sso/<provider id>/callback`. (Save the provider first to get its id, then copy the
   address from the page.)
2. In Polipan: **Add provider**, choose the type, and give the client ID and the client secret. The secret is stored
   encrypted with the master key and is never shown again.
3. Press **Test login**. It signs in at the provider and comes back to the SSO page with the result. It makes no
   session and no user.

| Type | Needs | Groups |
|---|---|---|
| OpenID Connect | `issuer_url` (the address that has `/.well-known/openid-configuration`, https) | The claim named in *Groups claim*, for example `groups` (add the scope that makes the provider send it). |
| Google | client ID and secret | None. Use *Default role*. |
| GitHub | client ID and secret. Scopes `read:user user:email read:org`. | Teams of the user as `org/team`. The organization must allow the OAuth app (*third-party access*). |

OIDC uses the authorization code flow with PKCE, `state` and `nonce`. The ID token is checked: signature (RS256 or
ES256), issuer, audience, expiry and nonce. The issuer address must not be a private address unless the organization
setting `allow_private_targets` is on.

## Who can sign in

- A user is found by the pair *(provider, subject)*. A new pair is linked to an existing user **only** when the provider says
  the email is verified. An unverified email is refused. For GitHub only the primary, verified email counts.
- **Allowed domains** limit the email domains. For Google the `hd` claim (the Workspace domain) must also be in the list:
  a mail address at your domain is not enough, because anybody can have a Google account.
- **Create users on first login** makes the account at the first login (just-in-time). With a **default role** the new user
  gets that role when no group mapping applies.
- **Safety rule:** for Google and GitHub, *create users* together with a *default role* is refused (422, `unsafe_jit`)
  while *Allowed domains* is empty. Without it everybody with an account would get access. The same rule is checked
  again at login. A default role can never be `owner`.
- A user who has no mapped role and no default role has no access: the login is refused with "no access" and no session is made.
  A new user is not created in this case.

## Groups to roles

In the *Group to role mapping* of a provider, map a group to a role for the whole organization or for one project.
It is applied at **every login**:

- The highest role wins in each scope.
- Roles made by a mapping have the source `sso` and are replaced at the next login. A role set by hand stays and is
  never changed by a mapping.
- When a user leaves the group, the access ends at the next login (an admin can end the sessions at once).
- The last owner is never demoted or removed by a mapping.
- Only an owner can map a group to `owner`. A project role cannot be `owner`.

## Require SSO and break-glass

Turn on **Require single sign-on** (the page warns you first). Passwords stop working for everybody, and the login page
shows only the provider buttons. You need at least one enabled provider before you can turn it on, and you cannot
delete or disable the last one while it is on.

**Break-glass:** an **owner** can still sign in with the password when the organization setting `allow_owner_password`
is on (the default). The owner needs two-factor login (TOTP). The login page then has a "Use a recovery login" link.
Every such login is written to the audit log as `auth.break_glass`. If the provider is down, this is the way in.
Turn `allow_owner_password` off to make SSO the only way, for owners too. Keep a second owner and a tested recovery
path before you do.

Users created by SSO have no password. Two-factor login of the organization (`require_2fa`) does not apply to them: the
provider is responsible for it.

## Audit

Provider changes (`sso.provider_create`, `sso.provider_update`, `sso.provider_delete`, `sso.mappings_replace`), logins
(`auth.sso_login`, `auth.sso_login_failed` with a reason), user creation and linking (`sso.user_create`,
`sso.identity_link`) and role changes by mappings (`role.change`, `sso.access_removed`, `sso.project_roles`) are written to the audit log.

## Problems

| Message on the login page | Cause |
|---|---|
| The login took too long or was started in another browser | The state cookie is missing or older than 10 minutes. Start again in the same browser. |
| The provider did not confirm your email address | `email_verified` is false or missing. |
| Your email domain is not allowed | The email domain, or for Google the `hd` claim, is not in *Allowed domains*. |
| There is no account for you | *Create users on first login* is off and no user has this verified email. |
| You have no access | No group mapping and no default role gives you a role. |
| The provider did not answer or refused the login | Wrong client secret, wrong redirect address, or the provider is down. The server log has the details. |
