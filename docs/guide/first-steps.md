# First steps

This page takes you from a new panel to a running app with HTTPS in about ten minutes.

## 1. Log in and look around

After the install you have one organization and one owner. The main menu has:

- **Projects**: apps and databases, grouped by project and environment.
- **Nodes**: your servers.
- **Backups**, **Alerts**, **Logs**, **Audit**, **Settings** (members, tokens, SSO).

## 2. Make a project

Press **New project** and enter a name, for example `blog`. A project starts with two environments:
`production` (protected) and `staging`. A protected environment lets only admins and owners change it.

## 3. Add an app

Press **New service** in the staging environment. There are three ways:

- **Docker image**: type an image name such as `nginx:1.27`. Give the container port.
- **Git repository**: connect GitHub, GitLab or Gitea first (**Settings → Git**), then pick a repository and a branch.
  Polipan builds with your `Dockerfile` or with Railpack.
- **Template**: pick WordPress, Plausible, n8n and others from the template list. See [Databases and templates](../operations/databases-and-templates.md).

Press **Deploy**. The deployment moves through `queued`, `building`, `pulling`, `health_checking`, `switching` and `live`.
The log of each step is shown live.

## 4. Add a domain

Open the service, tab **Domains**, press **Add domain**. Enter `blog.example.com`.
Point the DNS record to an ingress node as shown on the screen. When DNS is right, Polipan gets a certificate
and the status changes from `pending` to `issued`. If it fails, the error text is shown. See [Domains and HTTPS](domains.md).

## 5. Set variables and secrets

Open **Variables** to add settings such as `PORT=3000`. Add passwords as **secrets**: they are write-only,
masked in logs and encrypted. See [Variables and secrets](variables-secrets.md).

## 6. Deploy again, roll back

Push to the Git branch (or press **Deploy**). Each deploy makes a new **release**. If it breaks, open **Releases**
and press **Roll back** on an older release. See [Apps and deploys](apps-and-deploys.md).

## 7. Invite your team

**Settings → Members → Invite**. Choose a role: owner, admin, developer or viewer. See [Access control](access-control.md).
