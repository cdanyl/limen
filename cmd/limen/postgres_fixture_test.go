//go:build postgres

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/thecodearcher/limen"
	"github.com/thecodearcher/limen/access"
	sqladapter "github.com/thecodearcher/limen/adapters/sql"
	"github.com/thecodearcher/limen/plugins/organization"
)

// This explicit test target requires a PostgreSQL DSN. It never falls back to SQLite.
// Tables are generated from Limen's discovered schemas by the CLI's real generator.
type postgresFixture struct {
	db        *sql.DB
	adapter   limen.DatabaseAdapter
	auth      *limen.Limen
	api       organization.API
	config    *limen.SchemaConfig
	schemas   limen.SchemaDefinitionMap
	queries   *postgresQueries
	publicIDs bool
}

type postgresQueries struct {
	sync.Mutex
	statements []string
}

func (q *postgresQueries) LogQuery(_ context.Context, query string, _ any, _ time.Duration, _ error) {
	q.Lock()
	defer q.Unlock()
	q.statements = append(q.statements, query)
}
func (q *postgresQueries) reset() { q.Lock(); defer q.Unlock(); q.statements = nil }
func (q *postgresQueries) names() []string {
	q.Lock()
	defer q.Unlock()
	return append([]string(nil), q.statements...)
}

func newPostgresFixture(t *testing.T, publicIDs bool, decorate func(*sql.DB, *postgresQueries) limen.DatabaseAdapter) *postgresFixture {
	t.Helper()
	dsn := os.Getenv("LIMEN_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("LIMEN_POSTGRES_TEST_DSN is required for -tags=postgres")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin := stdlib.OpenDB(*cfg)
	if err := admin.PingContext(t.Context()); err != nil {
		admin.Close()
		t.Fatal("PostgreSQL test server unavailable")
	}
	schema := "limen_pr_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg = cfg.Copy()
	cfg.RuntimeParams["search_path"] = quoted
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Chdir(t.TempDir())
	ac := access.New(organization.DefaultStatements())
	roles, err := organization.DefaultRoles(ac)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := ac.NewRole("without-invitation-read", access.Permissions{"organization": {"read"}})
	if err != nil {
		t.Fatal(err)
	}
	roles = append(roles, restricted)
	options := []limen.SchemaConfigOption{}
	if publicIDs {
		options = append(options, limen.WithPublicIDs(limen.PublicIDConfig{
			Generator: func(context.Context, limen.SchemaName) (string, error) { return "pub_" + rand.Text(), nil },
			Matcher:   func(_ limen.SchemaName, value string) bool { return strings.HasPrefix(value, "pub_") },
		}))
	}
	queries := &postgresQueries{}
	var adapter limen.DatabaseAdapter = sqladapter.NewPostgreSQL(db).WithLogger(queries)
	if decorate != nil {
		adapter = decorate(db, queries)
	}
	schemaConfig := limen.NewDefaultSchemaConfig(options...)
	auth, err := limen.New(&limen.Config{
		BaseURL: "http://localhost", Secret: []byte("01234567890123456789012345678901"), Database: adapter, Schema: schemaConfig,
		CLI:     &limen.CLIConfig{Enabled: true},
		Plugins: []limen.Plugin{organization.New(organization.WithAccessControl(ac), organization.WithRoles(roles...), organization.WithMaxOrgPerUser(0), organization.WithCustomRoles(true))},
	})
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := loadConfig(filepath.Join(".limen", "schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	generator, err := newSQLMigrationGenerator(NewPostgresDriver(), discovered)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range orderSchemasByDependency(discovered.Schemas) {
		definition := discovered.Schemas[name]
		ddl, err := generator.generateCreateTable(&definition)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), ddl); err != nil {
			t.Fatalf("generated PostgreSQL schema %s: %v", name, err)
		}
	}
	return &postgresFixture{db: db, adapter: adapter, auth: auth, api: organization.Use(auth), config: schemaConfig, schemas: discovered.Schemas, queries: queries, publicIDs: publicIDs}
}

func (f *postgresFixture) user(t *testing.T, email string) *limen.User {
	t.Helper()
	schema := f.config.User
	payload := schema.ToStorage(&limen.User{Email: email})
	payload[schema.GetField(limen.SchemaCreatedAtField)] = time.Now()
	payload[schema.GetField(limen.SchemaUpdatedAtField)] = time.Now()
	if f.publicIDs {
		payload[schema.GetField(limen.SchemaPublicIDField)] = "pub_" + rand.Text()
	}
	if _, err := f.adapter.Create(t.Context(), schema.GetTableName(), payload); err != nil {
		t.Fatal(err)
	}
	row, err := f.adapter.FindOne(t.Context(), schema.GetTableName(), []limen.Where{limen.Eq(schema.GetEmailField(), email)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f.auth.NewUser(row)
}
func (f *postgresFixture) owner(t *testing.T, slug string) (*limen.User, *organization.Organization) {
	t.Helper()
	user := f.user(t, slug+"@example.test")
	org, err := f.api.CreateOrganization(t.Context(), user, &organization.CreateOrganizationRequest{Name: slug, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	return user, org
}
func (f *postgresFixture) definition(t *testing.T, table limen.SchemaTableName) limen.SchemaDefinition {
	t.Helper()
	for _, schema := range f.schemas {
		if schema.GetTableName() == table {
			return schema
		}
	}
	t.Fatal("schema not discovered")
	return limen.SchemaDefinition{}
}
func (f *postgresFixture) outwardID(t *testing.T, model limen.Model, table limen.SchemaTableName, fallback any) any {
	t.Helper()
	if !f.publicIDs {
		return fallback
	}
	definition := f.definition(t, table)
	value, ok := model.Raw()[f.column(t, definition, limen.SchemaPublicIDField)].(string)
	if !ok || value == "" {
		t.Fatal("public ID missing")
	}
	return value
}
func requirePostgresError(t *testing.T, err error, expected error) {
	t.Helper()
	if !errors.Is(err, expected) {
		t.Fatalf("unexpected error: got %v, want %v", err, expected)
	}
}

func (f *postgresFixture) column(t *testing.T, definition limen.SchemaDefinition, field limen.SchemaField) string {
	t.Helper()
	for _, column := range definition.Columns {
		if column.LogicalField == field {
			return column.Name
		}
	}
	t.Fatal("logical column not discovered")
	return ""
}
