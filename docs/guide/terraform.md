# Terraform

The provider `polipan` manages a Polipan panel as code. It uses the public API.

```hcl
terraform {
  required_providers {
    polipan = { source = "polipan/polipan" }
  }
}

provider "polipan" {
  url   = "https://panel.example.com" # or POLIPAN_URL
  token = var.polipan_token            # or POLIPAN_TOKEN, scope admin
}

resource "polipan_project" "blog" {
  name = "blog"
}
```

## Resources

`polipan_project`, `polipan_environment`, `polipan_service`, `polipan_domain`, `polipan_volume`, `polipan_variable`,
`polipan_secret`, `polipan_node_token`, `polipan_backup_target`, `polipan_backup_policy`, `polipan_alert_channel`,
`polipan_alert_rule`, `polipan_team`, `polipan_member`, `polipan_sso_provider`, `polipan_api_token`.
Data sources read projects, environments, services and nodes.

## Rules

- A second `terraform plan` after `apply` shows no change.
- `polipan_secret` values are `sensitive`. The API never returns a secret value, so the value in the state is the one
  you wrote.
- A service that is managed by `polipan.yaml` can not be changed from Terraform (`409 managed_by_git`).
- Import ids look like `project/environment/service`.

Run the acceptance tests with `sh terraform-provider/test/acc.sh` (needs Docker). They start a real server.
