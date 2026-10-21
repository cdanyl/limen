//go:build postgres

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thecodearcher/limen"
	"github.com/thecodearcher/limen/plugins/organization"
)

func TestPostgresInvitationExactLookup(t *testing.T) {
	for _, publicIDs := range []bool{false, true} {
		t.Run(fmt.Sprintf("public_ids_%t", publicIDs), func(t *testing.T) {
			f := newPostgresFixture(t, publicIDs, nil)
			owner, org := f.owner(t, "invitations")
			foreign, other := f.owner(t, "foreign")
			restricted := f.user(t, "restricted@example.test")
			membership, err := f.api.AddMember(t.Context(), org.ID, restricted.ID, "without-invitation-read")
			if err != nil {
				t.Fatal(err)
			}
			var target *organization.Invitation
			for _, state := range []string{"pending", "accepted", "rejected", "canceled", "expired"} {
				invitee := f.user(t, state+"@example.test")
				invitation, err := f.api.CreateInvitation(t.Context(), owner, org, &organization.CreateInvitationRequest{Email: invitee.Email, Role: "member"})
				if err != nil {
					t.Fatal(err)
				}
				switch state {
				case "accepted", "rejected":
					response := organization.InvitationResponseAccept
					if state == "rejected" {
						response = organization.InvitationResponseReject
					}
					invitation, err = f.api.RespondToInvitation(t.Context(), invitee, invitation.Token, response)
				case "canceled":
					invitation, err = f.api.CancelPendingInvitation(t.Context(), owner, org, invitation.ID)
				case "expired":
					definition := f.definition(t, organization.InvitationSchemaTableName)
					_, err = f.adapter.Update(t.Context(), definition.GetTableName(), []limen.Where{limen.Eq(f.column(t, definition, limen.SchemaIDField), invitation.ID)}, map[string]any{f.column(t, definition, organization.InvitationSchemaExpiresAtField): time.Now().Add(-time.Hour)})
				}
				if err != nil {
					t.Fatal(err)
				}
				key := f.outwardID(t, invitation, organization.InvitationSchemaTableName, invitation.ID)
				f.queries.reset()
				got, err := f.api.GetInvitationByID(t.Context(), owner, org, key)
				if err != nil || got == nil || got.ID != invitation.ID || got.OrganizationID != org.ID || got.Status != invitation.Status {
					t.Fatalf("exact %s invitation not read: %v", state, err)
				}
				reads := 0
				for _, query := range f.queries.names() {
					if strings.HasPrefix(query, "SELECT") && strings.Contains(query, "\"organization_invitations\"") {
						reads++
					}
				}
				if reads != 1 {
					t.Fatalf("exact lookup must issue one invitation read, got %d", reads)
				}
				if state == "expired" && (got.ExpiresAt == nil || !got.ExpiresAt.Before(time.Now())) {
					t.Fatal("expired invitation not retained")
				}
				requirePostgresError(t, func() error { _, err := f.api.GetInvitationByID(t.Context(), foreign, other, key); return err }(), limen.ErrRecordNotFound)
				requirePostgresError(t, func() error { _, err := f.api.GetInvitationByID(t.Context(), restricted, org, key); return err }(), organization.ErrInsufficientPermission)
				requirePostgresError(t, func() error { _, err := f.api.GetInvitationByID(t.Context(), foreign, org, key); return err }(), organization.ErrMemberNotInOrganization)
				target = invitation
			}
			// A role withdrawal and removed membership are read afresh, even after an allowed read.
			if err := f.api.AssignMemberRole(t.Context(), owner, org, membership.ID, "admin"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.api.GetInvitationByID(t.Context(), restricted, org, target.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.api.RevokeMemberRole(t.Context(), owner, org, membership.ID, "admin"); err != nil {
				t.Fatal(err)
			}
			_, err = f.api.GetInvitationByID(t.Context(), restricted, org, target.ID)
			requirePostgresError(t, err, organization.ErrInsufficientPermission)
			if err := f.api.RemoveMember(t.Context(), owner, org, membership.ID); err != nil {
				t.Fatal(err)
			}
			_, err = f.api.GetInvitationByID(t.Context(), restricted, org, target.ID)
			requirePostgresError(t, err, organization.ErrMemberNotInOrganization)
			_, err = f.api.GetInvitationByID(t.Context(), nil, org, target.ID)
			requirePostgresError(t, err, limen.ErrUnauthorized)
			_, err = f.api.GetInvitationByID(t.Context(), owner, nil, target.ID)
			requirePostgresError(t, err, limen.ErrRecordNotFound)
		})
	}
}
