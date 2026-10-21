package organization

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thecodearcher/limen"
)

func TestAssignAndRevokeMemberRole(t *testing.T) {
	t.Parallel()

	l, api := newTestOrgPlugin(t)
	owner, org, _ := seedOwnerOrg(t, l, api, "owner-roles@test.com", "Roles Org", "roles-org")
	memberUser := seedUser(t, l, "member-roles@test.com")
	inviteAndAccept(t, api, owner, org, memberUser, roleNameMember)
	memberID := memberIDForUser(t, api, org, memberUser)

	err := api.AssignMemberRole(t.Context(), owner, org, memberID, roleNameAdmin)
	require.NoError(t, err)
	assert.Equal(t, []string{roleNameAdmin, roleNameMember}, memberRoleNames(t, api, memberID))

	err = api.RevokeMemberRole(t.Context(), owner, org, memberID, roleNameAdmin)
	require.NoError(t, err)
	assert.Equal(t, []string{roleNameMember}, memberRoleNames(t, api, memberID))

	err = api.RevokeMemberRole(t.Context(), owner, org, memberID, roleNameMember)
	assert.ErrorIs(t, err, ErrMemberMustHaveAtLeastOneRole)
}

func TestLastOwnerProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		act  func(t *testing.T, api API, owner *limen.User, org *Organization, session *limen.Session) error
	}{
		{
			name: "remove",
			act: func(t *testing.T, api API, owner *limen.User, org *Organization, _ *limen.Session) error {
				t.Helper()
				return api.RemoveMember(t.Context(), owner, org, memberIDForUser(t, api, org, owner))
			},
		},
		{
			name: "leave",
			act: func(t *testing.T, api API, _ *limen.User, org *Organization, session *limen.Session) error {
				t.Helper()
				_, err := api.LeaveOrganization(t.Context(), session, org.ID)
				return err
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slug := fmt.Sprintf("last-owner-%d", i)
			l, api := newTestOrgPlugin(t)
			owner, org, session := seedOwnerOrg(t, l, api, slug+"@test.com", "Last Owner", slug)

			err := tt.act(t, api, owner, org, session)
			assert.ErrorIs(t, err, ErrCannotRemoveLastOwner)
		})
	}
}

func TestLeaveOrganization_ClearsActiveOrganization(t *testing.T) {
	t.Parallel()

	l, api := newTestOrgPlugin(t)
	owner, org, session := seedOwnerOrg(t, l, api, "owner-leave-clear@test.com", "Leave Clear", "leave-clear")
	coOwner := seedUser(t, l, "co-owner-leave-clear@test.com")
	inviteAndAccept(t, api, owner, org, coOwner, roleNameOwner)

	_, err := api.SetActiveOrganization(t.Context(), session, org)
	require.NoError(t, err)
	activeID, err := api.GetActiveOrganizationID(t.Context(), session)
	require.NoError(t, err)
	require.Equal(t, org.ID, activeID)

	_, err = api.LeaveOrganization(t.Context(), session, org.ID)
	require.NoError(t, err)

	activeID, err = api.GetActiveOrganizationID(t.Context(), session)
	require.NoError(t, err)
	assert.Nil(t, activeID)

	err = api.CheckMemberExistsInOrganization(t.Context(), org.ID, owner.ID)
	assert.ErrorIs(t, err, ErrMemberNotInOrganization)
}

func TestLeaveOrganization_DifferentActiveOrgUnchanged(t *testing.T) {
	t.Parallel()

	l, api := newTestOrgPlugin(t)
	owner, orgA, session := seedOwnerOrg(t, l, api, "owner-leave-other@test.com", "Org A", "leave-org-a")
	coOwner := seedUser(t, l, "co-owner-leave-other@test.com")
	inviteAndAccept(t, api, owner, orgA, coOwner, roleNameOwner)

	orgB, err := api.CreateOrganization(t.Context(), owner, &CreateOrganizationRequest{
		Name: "Org B",
		Slug: "leave-org-b",
	})
	require.NoError(t, err)

	_, err = api.SetActiveOrganization(t.Context(), session, orgB)
	require.NoError(t, err)

	result, err := api.LeaveOrganization(t.Context(), session, orgA.ID)
	require.NoError(t, err)
	assert.Nil(t, result)

	activeID, err := api.GetActiveOrganizationID(t.Context(), session)
	require.NoError(t, err)
	assert.Equal(t, orgB.ID, activeID)
}

func TestListAccountMemberships_BoundedPageAndIsolation(t *testing.T) {
	l, api := newTestOrgPlugin(t, WithMaxOrgPerUser(0))
	owner := seedUser(t, l, "page-owner@test.com")
	subject := seedUser(t, l, "page-subject@test.com")
	foreign := seedUser(t, l, "page-foreign@test.com")
	var first *Organization
	for i := 0; i < 100; i++ {
		org, err := api.CreateOrganization(t.Context(), owner, &CreateOrganizationRequest{Name: fmt.Sprintf("Own %03d", i), Slug: fmt.Sprintf("own-%03d", i)})
		require.NoError(t, err)
		if i == 0 {
			first = org
		}
		_, err = api.AddMember(t.Context(), org.ID, subject.ID, roleNameMember)
		require.NoError(t, err)
	}
	_, err := api.CreateOrganization(t.Context(), foreign, &CreateOrganizationRequest{Name: "Foreign", Slug: "foreign-page"})
	require.NoError(t, err)
	plugin := api.(*organizationPlugin)
	for _, limit := range []int{1, 2, 100} {
		opts := &limen.QueryOptions{Limit: limit, OrderBy: []limen.OrderBy{{Column: plugin.organizationSchema.GetNameField(), Direction: limen.OrderByAsc}}}
		page, err := api.ListAccountMemberships(t.Context(), subject, nil, opts)
		require.NoError(t, err)
		require.EqualValues(t, 100, page.Total)
		require.Len(t, page.Items, limit)
		require.Len(t, opts.OrderBy, 1, "caller options must not be mutated")
		for i, member := range page.Items {
			require.Equal(t, subject.ID, member.UserID)
			require.Same(t, subject, member.User)
			require.Equal(t, fmt.Sprintf("Own %03d", i), member.Organization.Name)
			require.Equal(t, []string{roleNameMember}, SortedRoleNames(member.Roles))
		}
	}
	opts := &limen.QueryOptions{Limit: 1, Offset: 100}
	empty, err := api.ListAccountMemberships(t.Context(), subject, nil, opts)
	require.NoError(t, err)
	require.Empty(t, empty.Items)
	require.EqualValues(t, 100, empty.Total)
	name := "Own 000"
	filtered, err := api.ListAccountMemberships(t.Context(), subject, &ListOrganizationsFilter{Name: &name}, &limen.QueryOptions{Limit: 2})
	require.NoError(t, err)
	require.EqualValues(t, 1, filtered.Total)
	require.Len(t, filtered.Items, 1)
	membership, err := api.GetMemberByUserID(t.Context(), first.ID, subject.ID)
	require.NoError(t, err)
	require.NoError(t, api.RemoveMember(t.Context(), owner, first, membership.ID))
	filtered, err = api.ListAccountMemberships(t.Context(), subject, &ListOrganizationsFilter{Name: &name}, &limen.QueryOptions{Limit: 2})
	require.NoError(t, err)
	require.Empty(t, filtered.Items)
	require.Zero(t, filtered.Total)
	_, err = api.ListAccountMemberships(t.Context(), subject, nil, &limen.QueryOptions{Limit: 101})
	require.Error(t, err)
	_, err = api.ListAccountMemberships(t.Context(), subject, nil, &limen.QueryOptions{Limit: 1, Offset: -1})
	require.Error(t, err)
}

func TestListAccountMemberships_DynamicRolesAndMissingUser(t *testing.T) {
	l, api := newTestOrgPlugin(t, WithCustomRoles(true))
	owner, org, _ := seedOwnerOrg(t, l, api, "dynamic-owner@test.com", "Dynamic", "dynamic-page")
	member := seedUser(t, l, "dynamic-member@test.com")
	joined, err := api.AddMember(t.Context(), org.ID, member.ID, roleNameMember)
	require.NoError(t, err)
	role, err := api.CreateOrganizationRole(t.Context(), owner, org, &CreateOrganizationRoleRequest{Name: "reader", Permissions: map[string][]string{"organization": {"read"}}})
	require.NoError(t, err)
	require.NoError(t, api.AssignMemberRole(t.Context(), owner, org, joined.ID, role.ID))
	page, err := api.ListAccountMemberships(t.Context(), member, nil, &limen.QueryOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, []string{"member", "reader"}, SortedRoleNames(page.Items[0].Roles))
	_, err = api.ListAccountMemberships(t.Context(), nil, nil, &limen.QueryOptions{Limit: 1})
	require.ErrorIs(t, err, limen.ErrUnauthorized)
}
