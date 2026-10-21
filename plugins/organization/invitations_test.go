package organization

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thecodearcher/limen"
)

func TestInvitation_CreateAndAccept(t *testing.T) {
	t.Parallel()

	l, api := newTestOrgPlugin(t)
	owner, org, _ := seedOwnerOrg(t, l, api, "owner-invite-accept@test.com", "Invite Accept", "invite-accept")
	invitee := seedUser(t, l, "invitee-accept@test.com")

	invitation, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
		Email: invitee.Email,
		Role:  roleNameMember,
	})
	require.NoError(t, err)
	assert.Equal(t, InvitationStatusPending, invitation.Status)

	accepted, err := api.RespondToInvitation(t.Context(), invitee, invitation.Token, InvitationResponseAccept)
	require.NoError(t, err)
	assert.Equal(t, InvitationStatusAccepted, accepted.Status)

	require.NoError(t, api.CheckMemberExistsInOrganization(t.Context(), org.ID, invitee.ID))
	assert.Equal(t, []string{roleNameMember}, memberRoleNames(t, api, memberIDForUser(t, api, org, invitee)))
}

func TestRespondToInvitation_Rejects(t *testing.T) {
	t.Parallel()

	const ttl = time.Second

	tests := []struct {
		name        string
		opts        []ConfigOption
		useSynctest bool
		advance     time.Duration
		wrongEmail  bool
		cancelFirst bool
		response    InvitationResponse
		wantErr     error
		noMember    bool
	}{
		{
			name:       "wrong email",
			wrongEmail: true,
			response:   InvitationResponseAccept,
			wantErr:    ErrInvitationEmailMismatch,
			noMember:   true,
		},
		{
			name:        "expired",
			opts:        []ConfigOption{WithInvitationExpiration(int(ttl.Seconds()))},
			useSynctest: true,
			advance:     ttl + time.Millisecond,
			response:    InvitationResponseAccept,
			wantErr:     limen.ErrRecordNotFound,
			noMember:    true,
		},
		{
			name:        "canceled",
			cancelFirst: true,
			response:    InvitationResponseAccept,
			wantErr:     limen.ErrRecordNotFound,
			noMember:    true,
		},
		{
			name:     "invalid response",
			response: InvitationResponse("maybe"),
			wantErr:  ErrInvalidInvitationResponse,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := func(t *testing.T) {
				slug := fmt.Sprintf("invite-reject-%d", i)
				l, api := newTestOrgPlugin(t, tt.opts...)
				owner, org, _ := seedOwnerOrg(t, l, api, slug+"-owner@test.com", "Invite Reject", slug)
				invitee := seedUser(t, l, slug+"-invitee@test.com")

				invitation, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
					Email: invitee.Email,
					Role:  roleNameMember,
				})
				require.NoError(t, err)

				if tt.cancelFirst {
					canceled, err := api.CancelPendingInvitation(t.Context(), owner, org, invitation.ID)
					require.NoError(t, err)
					assert.Equal(t, InvitationStatusCanceled, canceled.Status)
				}
				if tt.advance > 0 {
					time.Sleep(tt.advance)
				}

				responder := invitee
				if tt.wrongEmail {
					responder = seedUser(t, l, slug+"-other@test.com")
				}

				_, err = api.RespondToInvitation(t.Context(), responder, invitation.Token, tt.response)
				assert.ErrorIs(t, err, tt.wantErr)
				if tt.noMember {
					assert.ErrorIs(t, api.CheckMemberExistsInOrganization(t.Context(), org.ID, responder.ID), ErrMemberNotInOrganization)
				}
			}

			if tt.useSynctest {
				synctest.Test(t, run)
				return
			}
			run(t)
		})
	}
}

func TestInvitation_ResponseConcludesInvite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   InvitationResponse
		wantStatus InvitationStatus
	}{
		{name: "accept", response: InvitationResponseAccept, wantStatus: InvitationStatusAccepted},
		{name: "reject", response: InvitationResponseReject, wantStatus: InvitationStatusRejected},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slug := fmt.Sprintf("invite-done-%d", i)
			l, api := newTestOrgPlugin(t)
			owner, org, _ := seedOwnerOrg(t, l, api, slug+"-owner@test.com", "Invite Done", slug)
			invitee := seedUser(t, l, slug+"-invitee@test.com")

			invitation, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
				Email: invitee.Email,
				Role:  roleNameMember,
			})
			require.NoError(t, err)

			responded, err := api.RespondToInvitation(t.Context(), invitee, invitation.Token, tt.response)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, responded.Status)

			if tt.response == InvitationResponseReject {
				err = api.CheckMemberExistsInOrganization(t.Context(), org.ID, invitee.ID)
				assert.ErrorIs(t, err, ErrMemberNotInOrganization)
			}

			_, err = api.FindPendingInvitation(t.Context(), &FindPendingInvitationOptions{
				InvitationToken: invitation.Token,
			})
			assert.ErrorIs(t, err, limen.ErrRecordNotFound)

			_, err = api.RespondToInvitation(t.Context(), invitee, invitation.Token, tt.response)
			assert.ErrorIs(t, err, limen.ErrRecordNotFound)
		})
	}
}

func TestCreateInvitation_ExistingPending(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        []ConfigOption
		useSynctest bool
		run         func(t *testing.T, api API, owner *limen.User, org *Organization, existing *Invitation)
	}{
		{
			name: "rejects when resend disabled",
			run: func(t *testing.T, api API, owner *limen.User, org *Organization, _ *Invitation) {
				t.Helper()
				_, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
					Email:  "pending@test.com",
					Role:   roleNameMember,
					Resend: false,
				})
				assert.ErrorIs(t, err, ErrInvitationAlreadyExists)
			},
		},
		{
			name:        "resend refreshes existing invite",
			useSynctest: true,
			run: func(t *testing.T, api API, owner *limen.User, org *Organization, existing *Invitation) {
				t.Helper()
				before := *existing.ExpiresAt
				time.Sleep(time.Second)

				refreshed, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
					Email:  "pending@test.com",
					Role:   roleNameMember,
					Resend: true,
				})
				require.NoError(t, err)
				assert.Equal(t, existing.ID, refreshed.ID)
				assert.Equal(t, existing.Token, refreshed.Token)
				assert.Equal(t, InvitationStatusPending, refreshed.Status)
				require.NotNil(t, refreshed.ExpiresAt)
				assert.True(t, refreshed.ExpiresAt.After(before))
			},
		},
		{
			name: "cancel-on-new replaces pending invite",
			opts: []ConfigOption{WithCancelPendingInviteOnNewInvite(true)},
			run: func(t *testing.T, api API, owner *limen.User, org *Organization, existing *Invitation) {
				t.Helper()
				created, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
					Email:  "pending@test.com",
					Role:   roleNameMember,
					Resend: false,
				})
				require.NoError(t, err)
				assert.NotEqual(t, existing.Token, created.Token)
				assert.Equal(t, InvitationStatusPending, created.Status)

				_, err = api.FindPendingInvitation(t.Context(), &FindPendingInvitationOptions{
					InvitationToken: existing.Token,
				})
				assert.ErrorIs(t, err, limen.ErrRecordNotFound)

				found, err := api.FindPendingInvitation(t.Context(), &FindPendingInvitationOptions{
					InvitationToken: created.Token,
				})
				require.NoError(t, err)
				assert.Equal(t, created.ID, found.ID)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := func(t *testing.T) {
				slug := fmt.Sprintf("invite-pending-%d", i)
				l, api := newTestOrgPlugin(t, tt.opts...)
				owner, org, _ := seedOwnerOrg(t, l, api, slug+"-owner@test.com", "Invite Pending", slug)

				existing, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{
					Email: "pending@test.com",
					Role:  roleNameMember,
				})
				require.NoError(t, err)
				require.NotNil(t, existing.ExpiresAt)

				tt.run(t, api, owner, org, existing)
			}

			if tt.useSynctest {
				synctest.Test(t, run)
				return
			}
			run(t)
		})
	}
}

func TestGetInvitationByID_AuthorizationAndTerminalState(t *testing.T) {
	t.Parallel()
	l, api := newTestOrgPlugin(t)
	owner, org, _ := seedOwnerOrg(t, l, api, "exact-owner@test.com", "Exact", "exact")
	foreign, other, _ := seedOwnerOrg(t, l, api, "exact-foreign@test.com", "Other", "other-exact")
	invitee := seedUser(t, l, "exact-invitee@test.com")
	invitation, err := api.CreateInvitation(t.Context(), owner, org, &CreateInvitationRequest{Email: invitee.Email, Role: roleNameMember})
	require.NoError(t, err)
	_, err = api.CancelPendingInvitation(t.Context(), owner, org, invitation.ID)
	require.NoError(t, err)
	found, err := api.GetInvitationByID(t.Context(), owner, org, invitation.ID)
	require.NoError(t, err)
	require.Equal(t, InvitationStatusCanceled, found.Status)
	_, err = api.GetInvitationByID(t.Context(), foreign, other, invitation.ID)
	require.ErrorIs(t, err, limen.ErrRecordNotFound)
	_, err = api.GetInvitationByID(t.Context(), foreign, org, invitation.ID)
	require.ErrorIs(t, err, ErrMemberNotInOrganization)
	_, err = api.GetInvitationByID(t.Context(), nil, org, invitation.ID)
	require.ErrorIs(t, err, limen.ErrUnauthorized)
	_, err = api.GetInvitationByID(t.Context(), owner, nil, invitation.ID)
	require.ErrorIs(t, err, limen.ErrRecordNotFound)
}
