//go:build postgres

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thecodearcher/limen"
	gormadapter "github.com/thecodearcher/limen/adapters/gorm"
	"github.com/thecodearcher/limen/plugins/organization"
)

type countPostgresGorm struct {
	logger.Interface
	queries *postgresQueries
}

func (count countPostgresGorm) Trace(ctx context.Context, started time.Time, query func() (string, int64), err error) {
	statement, _ := query()
	if strings.HasPrefix(statement, "SELECT") {
		count.queries.LogQuery(ctx, "SELECT", nil, time.Since(started), err)
	}
}

func TestPostgresAccountMemberships(t *testing.T) {
	for _, driver := range []string{"sql", "gorm"} {
		for _, publicIDs := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/public_ids_%t", driver, publicIDs), func(t *testing.T) {
				var decorate func(*sql.DB, *postgresQueries) limen.DatabaseAdapter
				if driver == "gorm" {
					decorate = func(db *sql.DB, q *postgresQueries) limen.DatabaseAdapter {
						orm, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{Logger: countPostgresGorm{Interface: logger.Discard, queries: q}})
						if err != nil {
							t.Fatal(err)
						}
						return gormadapter.New(orm)
					}
				}
				f := newPostgresFixture(t, publicIDs, decorate)
				owner := f.user(t, "owner@example.test")
				subject := f.user(t, "subject@example.test")
				var first *organization.Organization
				for i := 0; i < 100; i++ {
					org, err := f.api.CreateOrganization(t.Context(), owner, &organization.CreateOrganizationRequest{Name: fmt.Sprintf("Own %03d", i), Slug: fmt.Sprintf("own-%03d", i)})
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						first = org
					}
					if _, err := f.api.AddMember(t.Context(), org.ID, subject.ID, "member"); err != nil {
						t.Fatal(err)
					}
				}
				_, foreign := f.owner(t, "foreign")
				definition := f.definition(t, organization.OrganizationSchemaTableName)
				nameColumn := f.column(t, definition, organization.OrganizationSchemaNameField)
				for _, limit := range []int{1, 2, 100} {
					opts := &limen.QueryOptions{Limit: limit, OrderBy: []limen.OrderBy{{Column: nameColumn, Direction: limen.OrderByAsc}}}
					f.queries.reset()
					page, err := f.api.ListAccountMemberships(t.Context(), subject, nil, opts)
					if err != nil {
						t.Fatal(err)
					}
					if page.Total != 100 || len(page.Items) != limit {
						t.Fatalf("incorrect page: total=%d length=%d", page.Total, len(page.Items))
					}
					queries := len(f.queries.names())
					if queries != 4 {
						t.Fatalf("page of %d must take four reads, got %d", limit, queries)
					}
					t.Logf("postgres-membership-cost driver=%s public_ids=%t page_size=%d reads=%d", driver, publicIDs, limit, queries)
					if len(opts.OrderBy) != 1 || opts.Offset != 0 || opts.Limit != limit {
						t.Fatal("caller pagination mutated")
					}
					for i, member := range page.Items {
						if member.UserID != subject.ID || member.User != subject || member.Organization == nil || member.Organization.ID == foreign.ID || member.Organization.Name != fmt.Sprintf("Own %03d", i) {
							t.Fatal("page leaks or mismatches membership/organization")
						}
						if publicIDs && f.outwardID(t, member.Organization, organization.OrganizationSchemaTableName, member.Organization.ID) == member.Organization.ID {
							t.Fatal("public organization identifier missing from page")
						}
						if got := organization.SortedRoleNames(member.Roles); len(got) != 1 || got[0] != "member" {
							t.Fatalf("wrong roles: %v", got)
						}
					}
				}
				page, err := f.api.ListAccountMemberships(t.Context(), subject, nil, &limen.QueryOptions{Limit: 2, Offset: 98, OrderBy: []limen.OrderBy{{Column: nameColumn, Direction: limen.OrderByAsc}}})
				if err != nil || len(page.Items) != 2 || page.Items[0].Organization.Name != "Own 098" {
					t.Fatalf("offset page failed: %v", err)
				}
				page, err = f.api.ListAccountMemberships(t.Context(), subject, nil, &limen.QueryOptions{Limit: 1, Offset: 100})
				if err != nil || page.Total != 100 || len(page.Items) != 0 {
					t.Fatalf("empty page failed: %v", err)
				}
				match := "Own 000"
				filtered, err := f.api.ListAccountMemberships(t.Context(), subject, &organization.ListOrganizationsFilter{Name: &match}, &limen.QueryOptions{Limit: 2})
				if err != nil || filtered.Total != 1 || len(filtered.Items) != 1 {
					t.Fatalf("filter/count disagreement: %v", err)
				}
				membership, err := f.api.GetMemberByUserID(t.Context(), first.ID, subject.ID)
				if err != nil {
					t.Fatal(err)
				}
				role, err := f.api.CreateOrganizationRole(t.Context(), owner, first, &organization.CreateOrganizationRoleRequest{Name: "reader", Permissions: map[string][]string{"organization": {"read"}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.api.AssignMemberRole(t.Context(), owner, first, membership.ID, role.ID); err != nil {
					t.Fatal(err)
				}
				filtered, err = f.api.ListAccountMemberships(t.Context(), subject, &organization.ListOrganizationsFilter{Name: &match}, &limen.QueryOptions{Limit: 2})
				if err != nil {
					t.Fatal(err)
				}
				if roles := organization.SortedRoleNames(filtered.Items[0].Roles); len(roles) != 2 || roles[0] != "member" || roles[1] != "reader" {
					t.Fatalf("dynamic role mismatch: %v", roles)
				}
				if err := f.api.RevokeMemberRole(t.Context(), owner, first, membership.ID, role.ID); err != nil {
					t.Fatal(err)
				}
				filtered, err = f.api.ListAccountMemberships(t.Context(), subject, &organization.ListOrganizationsFilter{Name: &match}, &limen.QueryOptions{Limit: 2})
				if err != nil || len(filtered.Items[0].Roles) != 1 {
					t.Fatalf("revoked role retained: %v", err)
				}
				if err := f.api.RemoveMember(t.Context(), owner, first, membership.ID); err != nil {
					t.Fatal(err)
				}
				filtered, err = f.api.ListAccountMemberships(t.Context(), subject, &organization.ListOrganizationsFilter{Name: &match}, &limen.QueryOptions{Limit: 2})
				if err != nil || filtered.Total != 0 || len(filtered.Items) != 0 {
					t.Fatalf("removed membership retained: %v", err)
				}
				_, err = f.api.ListAccountMemberships(t.Context(), nil, nil, &limen.QueryOptions{Limit: 1})
				requirePostgresError(t, err, limen.ErrUnauthorized)
				for _, opts := range []*limen.QueryOptions{{Limit: 101}, {Limit: 1, Offset: -1}} {
					if _, err := f.api.ListAccountMemberships(t.Context(), subject, nil, opts); err == nil {
						t.Fatal("invalid pagination accepted")
					}
				}
			})
		}
	}
}
