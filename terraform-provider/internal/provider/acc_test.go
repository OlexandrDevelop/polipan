// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// The acceptance tests need a live server: run terraform-provider/test/acc.sh. They are skipped
// unless TF_ACC=1, POLIPAN_URL and POLIPAN_TOKEN are set.

var accFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"polipan": providerserver.NewProtocol6WithError(New("test")()),
}

func accPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("POLIPAN_URL") == "" || os.Getenv("POLIPAN_TOKEN") == "" {
		t.Fatal("POLIPAN_URL and POLIPAN_TOKEN must be set for acceptance tests")
	}
}

func accClient() *client.Client {
	return client.New(os.Getenv("POLIPAN_URL"), os.Getenv("POLIPAN_TOKEN"))
}

func stateAttr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes[key], nil
}

const accProjectService = `
resource "polipan_project" "p" {
  name = "acc-project-%[1]s"
  slug = "acc-project-%[1]s"
}

resource "polipan_environment" "e" {
  project_id = polipan_project.p.id
  name       = "qa"
  kind       = "custom"
}

resource "polipan_service" "web" {
  environment_id = polipan_environment.e.id
  name           = "web"
  image          = "nginx:1.27"
  port           = 80
}

resource "polipan_volume" "data" {
  service_id = polipan_service.web.id
  name       = "data"
  mount_path = "/data"
}

resource "polipan_domain" "d" {
  service_id = polipan_service.web.id
  host       = "web-%[1]s.example.com"
}

resource "polipan_variable" "v" {
  project_id = polipan_project.p.id
  name       = "LOG_LEVEL"
  value      = "debug"
}

resource "polipan_team" "t" {
  name = "acc-team-%[1]s"
}

resource "polipan_api_token" "tok" {
  name   = "acc-token-%[1]s"
  scopes = ["read"]
}

data "polipan_project" "by_slug" {
  slug = polipan_project.p.slug
}

data "polipan_service" "by_name" {
  environment_id = polipan_environment.e.id
  name           = polipan_service.web.name
}
`

// AC-TF-01: a project and a service are created, and the second plan shows no change.
// The test framework plans again after every apply and fails when the plan is not empty. The
// second step plans once more on its own (PlanOnly), and the import steps check REQ-TF-004.
func TestAccProjectAndService(t *testing.T) {
	accPreCheck(t)
	sfx := strings.ToLower(fmt.Sprint(os.Getpid()))
	cfg := fmt.Sprintf(accProjectService, sfx)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("polipan_project.p", "id"),
					resource.TestCheckResourceAttr("polipan_service.web", "image", "nginx:1.27"),
					resource.TestCheckResourceAttr("polipan_service.web", "restart_policy", "always"),
					resource.TestCheckResourceAttr("polipan_domain.d", "path_prefix", "/"),
					resource.TestCheckResourceAttr("polipan_domain.d", "container_port", "80"),
					resource.TestCheckResourceAttr("data.polipan_project.by_slug", "name", "acc-project-"+sfx),
					resource.TestCheckResourceAttrPair("data.polipan_service.by_name", "id", "polipan_service.web", "id"),
					// The objects exist in the API.
					func(s *terraform.State) error {
						id, err := stateAttr(s, "polipan_service.web", "id")
						if err != nil {
							return err
						}
						_, err = accClient().Get(context.Background(), "/services/"+id)
						return err
					},
				),
			},
			{Config: cfg, PlanOnly: true}, // second plan: no change
			{ResourceName: "polipan_project.p", ImportState: true, ImportStateVerify: true},
			{ResourceName: "polipan_environment.e", ImportState: true, ImportStateVerify: true},
			{ResourceName: "polipan_service.web", ImportState: true, ImportStateVerify: true},
			{ResourceName: "polipan_team.t", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "polipan_domain.d", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					sid, _ := stateAttr(s, "polipan_service.web", "id")
					id, _ := stateAttr(s, "polipan_domain.d", "id")
					return sid + "/" + id, nil
				},
			},
			{
				ResourceName: "polipan_volume.data", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					sid, _ := stateAttr(s, "polipan_service.web", "id")
					id, _ := stateAttr(s, "polipan_volume.data", "id")
					return sid + "/" + id, nil
				},
			},
			{
				ResourceName: "polipan_variable.v", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					pid, _ := stateAttr(s, "polipan_project.p", "id")
					id, _ := stateAttr(s, "polipan_variable.v", "id")
					return pid + "/" + id, nil
				},
			},
			{
				// An update in place: a new image and a renamed team.
				Config: strings.ReplaceAll(strings.ReplaceAll(cfg, "nginx:1.27", "nginx:1.26"), `name = "acc-team-`, `name = "acc-team2-`),
				Check:  resource.TestCheckResourceAttr("polipan_service.web", "image", "nginx:1.26"),
			},
		},
	})
}

const accSecret = `
resource "polipan_project" "p" {
  name = "acc-secret-%[1]s"
  slug = "acc-secret-%[1]s"
}

resource "polipan_secret" "s" {
  project_id = polipan_project.p.id
  name       = "DB_PASSWORD"
  value      = "%[2]s"
}
`

// AC-TF-02: the secret value is sensitive in the state, and the API returns no value.
func TestAccSecretIsSensitive(t *testing.T) {
	accPreCheck(t)
	sfx := strings.ToLower(fmt.Sprint(os.Getpid()))
	secret1, secret2 := "s3cr3t-value-one-"+sfx, "s3cr3t-value-two-"+sfx
	var secretID, projectID string
	apiHasNoValue := func(want string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			projectID, _ = stateAttr(s, "polipan_project.p", "id")
			secretID, _ = stateAttr(s, "polipan_secret.s", "id")
			items, err := accClient().List(context.Background(), "/projects/"+projectID+"/secrets")
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(items)
			if strings.Contains(string(raw), want) {
				return fmt.Errorf("the API answer has the secret value: %s", raw)
			}
			for _, it := range items {
				if _, has := it["value"]; has {
					return fmt.Errorf("the API answer has a value field: %v", it)
				}
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(accSecret, sfx, secret1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectSensitiveValue("polipan_secret.s", tfjsonpath.New("value")),
					statecheck.ExpectKnownValue("polipan_secret.s", tfjsonpath.New("version"), knownvalue.Int64Exact(1)),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("polipan_secret.s", "value", secret1), // the input only
					apiHasNoValue(secret1),
				),
			},
			{Config: fmt.Sprintf(accSecret, sfx, secret1), PlanOnly: true},
			{
				// A new value makes version 2.
				Config: fmt.Sprintf(accSecret, sfx, secret2),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectSensitiveValue("polipan_secret.s", tfjsonpath.New("value")),
					statecheck.ExpectKnownValue("polipan_secret.s", tfjsonpath.New("version"), knownvalue.Int64Exact(2)),
				},
				Check: apiHasNoValue(secret2),
			},
			{
				// Drift: somebody sets a new value outside Terraform. The version changes, so the plan is not empty.
				PreConfig: func() {
					if _, err := accClient().Put(context.Background(), "/secrets/"+secretID, client.Obj{"value": "changed-outside"}); err != nil {
						t.Fatal(err)
					}
				},
				Config:             fmt.Sprintf(accSecret, sfx, secret2),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Apply writes the value from the config again.
				Config: fmt.Sprintf(accSecret, sfx, secret2),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("polipan_secret.s", tfjsonpath.New("version"), knownvalue.Int64Exact(4)),
				},
			},
			{
				ResourceName: "polipan_secret.s", ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return projectID + "/" + secretID, nil },
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"},
			},
		},
	})
}
