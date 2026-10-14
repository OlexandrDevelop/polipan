// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"strings"
	"testing"
)

// A Compose file that needs passwords that nobody gave: they are generated and stored as secrets. The answer never
// has a value or a marker.
func TestComposeImportGeneratesSecrets(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Blog")
	doc := "services:\n  app:\n    image: app:1\n    environment:\n      DB_HOST: localhost\n      DB_PASSWORD: ${DB_PASSWORD:?set it}\n  postgres:\n    image: postgres:16\n    environment:\n      POSTGRES_PASSWORD: ${DB_PASSWORD:?set it}\n  redis:\n    image: redis:7\n    command: redis-server --requirepass ${REDIS_PASSWORD:?set it}\n"
	dry := c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose?dry_run=true", "application/yaml", doc)
	if dry.Code != 200 || e.count(`SELECT count(*) FROM services`) != 0 {
		t.Fatalf("dry run: %d %s", dry.Code, dry.Body)
	}
	real := c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose", "application/yaml", doc)
	if real.Code != 200 {
		t.Fatalf("import: %d %s", real.Code, real.Body)
	}
	for _, rec := range []string{dry.Body.String(), real.Body.String()} {
		if strings.Contains(rec, "__polipan_gen_") || !strings.Contains(rec, `"generated"`) {
			t.Errorf("the answer must have the generated losses and no marker: %s", rec)
		}
	}
	if n := e.count(`SELECT count(*) FROM secrets WHERE name IN ('DB_PASSWORD', 'POSTGRES_PASSWORD', 'REDIS_PASSWORD')`); n != 3 {
		t.Errorf("want 3 secrets, got %d", n)
	}
}
