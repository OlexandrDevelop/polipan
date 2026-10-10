// SPDX-License-Identifier: Apache-2.0

// Package dbengine has the rules of managed databases (C02): the engines, their images and
// versions, the variables that make the official image start with our credentials, and the
// connection fields that other services use as ${{db.<name>.FIELD}}.
package dbengine

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Engine is a database engine of REQ-DB-001.
type Engine struct {
	Name        string
	Image       string   // image without tag
	DefaultTag  string   // default version
	Majors      []int    // supported major versions
	Port        int      // port of the engine
	DataPath    string   // path of the data volume in the container
	Scheme      string   // scheme of the connection string
	Env         []EnvVar // variables that start the image with DB_USER, DB_NAME and DB_PASSWORD
	Command     []string // command, when the image needs one (redis)
	MinMemoryMB int
}

// EnvVar is a variable of the database service. Value may use ${{DB_USER}}, ${{DB_NAME}} and ${{DB_PASSWORD}}.
type EnvVar struct{ Name, Value string }

// Engines by name.
var Engines = map[string]*Engine{
	"postgres": {Name: "postgres", Image: "postgres", DefaultTag: "16", Majors: []int{13, 14, 15, 16, 17}, Port: 5432,
		DataPath: "/var/lib/postgresql/data", Scheme: "postgres", Env: []EnvVar{
			{"POSTGRES_USER", "${{DB_USER}}"}, {"POSTGRES_DB", "${{DB_NAME}}"}, {"POSTGRES_PASSWORD", "${{DB_PASSWORD}}"},
			{"PGDATA", "/var/lib/postgresql/data/pgdata"}}},
	"mysql": {Name: "mysql", Image: "mysql", DefaultTag: "8.4", Majors: []int{8, 9}, Port: 3306,
		DataPath: "/var/lib/mysql", Scheme: "mysql", Env: []EnvVar{
			{"MYSQL_USER", "${{DB_USER}}"}, {"MYSQL_DATABASE", "${{DB_NAME}}"}, {"MYSQL_PASSWORD", "${{DB_PASSWORD}}"},
			{"MYSQL_ROOT_PASSWORD", "${{DB_PASSWORD}}"}}},
	"mariadb": {Name: "mariadb", Image: "mariadb", DefaultTag: "11", Majors: []int{10, 11}, Port: 3306,
		DataPath: "/var/lib/mysql", Scheme: "mysql", Env: []EnvVar{
			{"MARIADB_USER", "${{DB_USER}}"}, {"MARIADB_DATABASE", "${{DB_NAME}}"}, {"MARIADB_PASSWORD", "${{DB_PASSWORD}}"},
			{"MARIADB_ROOT_PASSWORD", "${{DB_PASSWORD}}"}}},
	"mongo": {Name: "mongo", Image: "mongo", DefaultTag: "7", Majors: []int{6, 7, 8}, Port: 27017,
		DataPath: "/data/db", Scheme: "mongodb", Env: []EnvVar{
			{"MONGO_INITDB_ROOT_USERNAME", "${{DB_USER}}"}, {"MONGO_INITDB_ROOT_PASSWORD", "${{DB_PASSWORD}}"},
			{"MONGO_INITDB_DATABASE", "${{DB_NAME}}"}}},
	"redis": {Name: "redis", Image: "redis", DefaultTag: "7", Majors: []int{6, 7, 8}, Port: 6379,
		DataPath: "/data", Scheme: "redis",
		// The password comes from the secret DB_PASSWORD, which is an environment variable of the container.
		Command: []string{"sh", "-c", `exec redis-server --requirepass "$DB_PASSWORD" --appendonly yes`}},
}

// Names lists the engine names.
var Names = []string{"postgres", "mysql", "mariadb", "mongo", "redis"}

// tagRe is a plain version tag with an optional variant: 16, 16.4, 8.4.3, 7-alpine, 11.4-noble.
var tagRe = regexp.MustCompile(`^(\d+)(?:\.\d+){0,2}(?:-(?:alpine|bookworm|bullseye|noble|jammy|oracle))?$`)

// Get returns the engine or nil.
func Get(name string) *Engine { return Engines[name] }

// Image returns the image reference for a version. An empty version means the default.
func (e *Engine) ImageRef(version string) (string, error) {
	if version == "" {
		version = e.DefaultTag
	}
	major, err := e.Major(version)
	if err != nil {
		return "", err
	}
	_ = major
	return e.Image + ":" + version, nil
}

// Major returns the major version of a tag and checks that the engine supports it (REQ-DB-001).
func (e *Engine) Major(tag string) (int, error) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, fmt.Errorf("version %q is not a plain %s version tag, for example %s", tag, e.Name, e.DefaultTag)
	}
	n, _ := strconv.Atoi(m[1])
	for _, s := range e.Majors {
		if s == n {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s version %d is not supported (supported: %v)", e.Name, n, e.Majors)
}

// TagOf returns the tag of an image reference of the engine, or "" when the image is not of the engine.
func (e *Engine) TagOf(image string) string {
	rest, ok := strings.CutPrefix(image, e.Image+":")
	if !ok || strings.Contains(rest, "@") {
		return ""
	}
	return rest
}

// CheckVersionChange refuses a major version change (REQ-DB-007). A minor change is allowed.
func (e *Engine) CheckVersionChange(oldImage, newImage string) error {
	oldTag, newTag := e.TagOf(oldImage), e.TagOf(newImage)
	if newTag == "" {
		return fmt.Errorf("the image of a %s database must be %s:<version>", e.Name, e.Image)
	}
	nm, err := e.Major(newTag)
	if err != nil {
		return err
	}
	if oldTag == "" {
		return nil
	}
	om, err := e.Major(oldTag)
	if err != nil {
		return nil
	}
	if om != nm {
		return fmt.Errorf("the major version cannot change from %d to %d in place. Use \"dump and restore into a new service\"", om, nm)
	}
	return nil
}

// Field is a connection field of ${{db.<name>.FIELD}} (REQ-DB-004).
var Fields = []string{"URL", "HOST", "PORT", "USER", "PASSWORD", "NAME"}

// Alias is the stable network name of a service inside the Docker network of the node.
// A placement has its own alias too, but it changes with every deployment.
func Alias(serviceID string) string {
	return "svc-" + strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(serviceID, "svc_"), "_", "-"))
}

// URL builds the connection string. The password is a reference that is resolved later, so the
// value of the variable stays a template and never holds the password in plain text.
func (e *Engine) URL(host, user, name, passwordRef string) string {
	u := url.URL{Scheme: e.Scheme, Host: host + ":" + strconv.Itoa(e.Port)}
	switch e.Name {
	case "redis":
		return "redis://default:" + passwordRef + "@" + u.Host
	case "mongo":
		return "mongodb://" + url.PathEscape(user) + ":" + passwordRef + "@" + u.Host + "/" + name + "?authSource=admin"
	}
	return u.Scheme + "://" + url.PathEscape(user) + ":" + passwordRef + "@" + u.Host + "/" + name
}

// DefaultUser and DefaultName are the user and the database name of a new database service.
func DefaultUser(engine string) string {
	if engine == "mongo" {
		return "root"
	}
	return "app"
}

// DefaultName is the database name of a new database service.
func DefaultName(engine string) string {
	if engine == "redis" {
		return "0"
	}
	return "app"
}

// PasswordLength is the length of a generated password (REQ-DB-003).
const PasswordLength = 32
