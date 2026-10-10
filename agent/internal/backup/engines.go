// SPDX-License-Identifier: Apache-2.0

package backup

import "fmt"

// engineSpec has the shell commands of one engine. They run in the container of the database with
// "sh -c" and read the user, the database and the password from the environment of the container
// (DB_USER, DB_NAME and DB_PASSWORD), so a password is never in an argument that Polipan sends.
type engineSpec struct {
	name     string
	file     string // name of the dump in the snapshot
	dump     string // writes the dump to stdout
	restore  string // reads a dump from stdin and loads it into this database
	manifest string // prints the manifest numbers, "name=value" per line
}

// pgEnv is the connection of a Polipan database service or of the control-plane database.
const (
	pgEnv  = `PGPASSWORD="$DB_PASSWORD" `
	pgConn = `-U "$DB_USER" -d "$DB_NAME"`
	cpEnv  = `PGPASSWORD="$POSTGRES_PASSWORD" `
	cpConn = `-U "$POSTGRES_USER" -d "$POSTGRES_DB"`
)

const (
	pgTables = `select count(*) from information_schema.tables where table_type='BASE TABLE' and table_schema not in ('pg_catalog','information_schema')`
	pgRows   = `select coalesce(sum((xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', table_schema, table_name), false, true, '')))[1]::text::bigint),0) ` +
		`from information_schema.tables where table_type='BASE TABLE' and table_schema not in ('pg_catalog','information_schema')`
)

// mysqlClient picks the client of MySQL or MariaDB: the images of MariaDB 11 have the new names.
func mysqlTool(oldName, newName string) string {
	return fmt.Sprintf(`T=$(command -v %s || command -v %s)`, newName, oldName)
}

var engines = map[string]engineSpec{
	"postgres": {
		name: "postgres", file: "dump.pgdump",
		dump:     pgEnv + `exec pg_dump ` + pgConn + ` -Fc`,
		restore:  pgEnv + `exec pg_restore ` + pgConn + ` --clean --if-exists --no-owner --no-privileges`,
		manifest: pgEnv + `psql ` + pgConn + ` -tA -c "select 'tables=' || (` + pgTables + `)" -c "select 'rows=' || (` + pgRows + `)"`,
	},
	"controlplane": {
		name: "controlplane", file: "dump.pgdump",
		dump:     cpEnv + `exec pg_dump ` + cpConn + ` -Fc`,
		restore:  "", // never restored by the agent: see the manual restore in the install docs
		manifest: cpEnv + `psql ` + cpConn + ` -tA -c "select 'tables=' || (` + pgTables + `)" -c "select 'rows=' || (` + pgRows + `)"`,
	},
	"mysql": {
		name: "mysql", file: "dump.sql",
		dump:     mysqlTool("mysqldump", "mysqldump") + `; MYSQL_PWD="$DB_PASSWORD" exec "$T" -u root --single-transaction --routines --triggers --no-tablespaces --databases "$DB_NAME"`,
		restore:  mysqlTool("mysql", "mysql") + `; MYSQL_PWD="$DB_PASSWORD" exec "$T" -u root`,
		manifest: mysqlTool("mysql", "mysql") + `; echo "tables=$(MYSQL_PWD="$DB_PASSWORD" "$T" -N -u root -e "select count(*) from information_schema.tables where table_schema='$DB_NAME'")"`,
	},
	"mariadb": {
		name: "mariadb", file: "dump.sql",
		dump:     mysqlTool("mysqldump", "mariadb-dump") + `; MYSQL_PWD="$DB_PASSWORD" exec "$T" -u root --single-transaction --routines --triggers --databases "$DB_NAME"`,
		restore:  mysqlTool("mysql", "mariadb") + `; MYSQL_PWD="$DB_PASSWORD" exec "$T" -u root`,
		manifest: mysqlTool("mysql", "mariadb") + `; echo "tables=$(MYSQL_PWD="$DB_PASSWORD" "$T" -N -u root -e "select count(*) from information_schema.tables where table_schema='$DB_NAME'")"`,
	},
	"mongo": {
		name: "mongo", file: "dump.archive",
		dump:    `exec mongodump --archive --username "$DB_USER" --password "$DB_PASSWORD" --authenticationDatabase admin --quiet`,
		restore: `exec mongorestore --archive --drop --username "$DB_USER" --password "$DB_PASSWORD" --authenticationDatabase admin --quiet`,
		manifest: `echo "collections=$(mongosh --quiet --username "$DB_USER" --password "$DB_PASSWORD" --authenticationDatabase admin --eval ` +
			`'let n=0; db.adminCommand({listDatabases:1}).databases.forEach(d=>{ if(!["admin","local","config"].includes(d.name)) n+=db.getSiblingDB(d.name).getCollectionNames().length }); print(n)')"`,
	},
	"redis": {
		name: "redis", file: "dump.rdb",
		// redis-cli writes a file in the container (not on the node). Its messages go to stderr.
		dump:     `REDISCLI_AUTH="$DB_PASSWORD" redis-cli --rdb /tmp/pp-backup.rdb >&2 && cat /tmp/pp-backup.rdb; c=$?; rm -f /tmp/pp-backup.rdb; exit $c`,
		manifest: `echo "keys=$(REDISCLI_AUTH="$DB_PASSWORD" redis-cli dbsize)"`,
	},
}
