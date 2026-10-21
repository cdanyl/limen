package organization

import (
	"context"
	"slices"

	"github.com/thecodearcher/limen"
	"github.com/thecodearcher/limen/access"
)

func (o *organizationPlugin) AddMember(ctx context.Context, organizationID, userID, role any) (*Member, error) {
	organization, err := o.GetOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	user, err := o.core.DBAction.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if err := o.checkMemberCanJoin(ctx, organization, user); err != nil {
		return nil, err
	}

	if o.hooks.BeforeAddMember != nil {
		if err := o.hooks.BeforeAddMember(ctx, organization, user, role); err != nil {
			return nil, err
		}
	}

	member, err := o.insertMemberWithRole(ctx, organization, user, role)
	if err != nil {
		return nil, err
	}

	if o.hooks.AfterAddMember != nil {
		o.hooks.AfterAddMember(ctx, organization, user, member)
	}
	return member, nil
}

func (o *organizationPlugin) AssignMemberRole(ctx context.Context, user *limen.User, organization *Organization, memberID any, roleToAssign any) error {
	actor, err := o.loadMemberAccess(ctx, organization.ID, user.ID)
	if err != nil {
		return err
	}
	if err := actor.requirePermissions(perms("member:update")); err != nil {
		return err
	}

	member, err := o.GetMemberByID(ctx, organization, memberID)
	if err != nil {
		return err
	}

	if err := o.validateMemberBelongsToOrganization(member, organization); err != nil {
		return err
	}

	resolvedRoles, err := o.resolveRoles(ctx, organization, []any{roleToAssign})
	if err != nil {
		return err
	}
	role := resolvedRoles[0]

	if err := o.ensureCanGrantRoles(actor, []*access.Role{role}); err != nil {
		return err
	}

	if o.hooks.BeforeAssignMemberRole != nil {
		if err := o.hooks.BeforeAssignMemberRole(ctx, user, organization, member, role); err != nil {
			return err
		}
	}

	if err := o.assignMemberRole(ctx, member, role); err != nil {
		return err
	}

	if o.hooks.AfterAssignMemberRole != nil {
		o.hooks.AfterAssignMemberRole(ctx, user, organization, member, role)
	}
	return nil
}

func (o *organizationPlugin) RevokeMemberRole(ctx context.Context, user *limen.User, organization *Organization, memberID any, roleToRevoke any) error {
	actor, err := o.loadMemberAccess(ctx, organization.ID, user.ID)
	if err != nil {
		return err
	}
	if err := actor.requirePermissions(perms("member:update")); err != nil {
		return err
	}

	member, err := o.GetMemberByID(ctx, organization, memberID)
	if err != nil {
		return err
	}

	if err := o.validateMemberBelongsToOrganization(member, organization); err != nil {
		return err
	}

	resolvedRole, err := o.resolveRoles(ctx, organization, []any{roleToRevoke})
	if err != nil {
		return err
	}

	role := resolvedRole[0]
	if err := o.checkMemberHasRole(ctx, member, role); err != nil {
		return err
	}

	existingRolesCount, err := o.core.Count(ctx, o.memberRoleSchema, []limen.Where{
		limen.Eq(o.memberRoleSchema.GetMemberIDField(), member.ID),
	})

	if err != nil {
		return err
	}

	if existingRolesCount == 1 {
		return ErrMemberMustHaveAtLeastOneRole
	}

	if err := o.ensureCanActOnSpecialRoles(actor, []*access.Role{role}); err != nil {
		return err
	}

	if err := o.validateOwnerRoleCanBeRemoved(ctx, organization, role); err != nil {
		return err
	}

	if o.hooks.BeforeRevokeMemberRole != nil {
		if err := o.hooks.BeforeRevokeMemberRole(ctx, user, organization, member, role); err != nil {
			return err
		}
	}

	result, err := o.core.DeleteWithResult(ctx, o.memberRoleSchema, o.memberRoleConditions(member, role))
	if err != nil {
		return err
	}

	if result.RowsAffected == 0 {
		return ErrMemberRoleNotAssigned
	}

	if o.hooks.AfterRevokeMemberRole != nil {
		o.hooks.AfterRevokeMemberRole(ctx, user, organization, member, role)
	}
	return nil
}

func (o *organizationPlugin) GetMemberByUserID(ctx context.Context, organizationID, userID any) (*Member, error) {
	member, err := o.core.FindOne(ctx, o.memberSchema, []limen.Where{
		limen.Eq(o.memberSchema.GetOrganizationIDField(), organizationID),
		limen.Eq(o.memberSchema.GetUserIDField(), userID),
	}, nil)
	if err != nil {
		return nil, err
	}

	return member.(*Member), nil
}

func (o *organizationPlugin) GetMemberByID(ctx context.Context, organization *Organization, memberID any) (*Member, error) {
	member, err := o.core.FindOne(ctx, o.memberSchema, []limen.Where{
		limen.Eq(o.memberSchema.GetOrganizationIDField(), organization.ID),
		limen.Eq(o.memberSchema.GetIDField(), memberID),
	}, nil)
	if err != nil {
		return nil, err
	}

	return member.(*Member), nil
}

func (o *organizationPlugin) RemoveMember(ctx context.Context, user *limen.User, organization *Organization, memberID any) error {
	actor, err := o.loadMemberAccess(ctx, organization.ID, user.ID)
	if err != nil {
		return err
	}
	if err := actor.requirePermissions(perms("member:delete")); err != nil {
		return err
	}

	member, err := o.GetMemberByID(ctx, organization, memberID)
	if err != nil {
		return err
	}

	if err := o.validateMemberBelongsToOrganization(member, organization); err != nil {
		return err
	}

	memberRoles, err := o.GetMemberRoles(ctx, member.ID)
	if err != nil {
		return err
	}

	if err := o.ensureCanActOnSpecialRoles(actor, memberRoles); err != nil {
		return err
	}

	if err := o.validateOwnerRoleCanBeRemoved(ctx, organization, memberRoles...); err != nil {
		return err
	}

	if o.hooks.BeforeRemoveMember != nil {
		if err := o.hooks.BeforeRemoveMember(ctx, user, organization, member); err != nil {
			return err
		}
	}

	if err := o.deleteMember(ctx, organization, member); err != nil {
		return err
	}

	if o.hooks.AfterRemoveMember != nil {
		o.hooks.AfterRemoveMember(ctx, user, organization, member)
	}
	return nil
}

func (o *organizationPlugin) LeaveOrganization(ctx context.Context, session *limen.Session, organizationID any) (*limen.SessionResult, error) {
	organization, err := o.GetOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	member, err := o.GetMemberByUserID(ctx, organization.ID, session.UserID)
	if err != nil {
		return nil, err
	}

	if err := o.validateMemberBelongsToOrganization(member, organization); err != nil {
		return nil, err
	}

	memberRoles, err := o.GetMemberRoles(ctx, member.ID)
	if err != nil {
		return nil, err
	}

	if err := o.validateOwnerRoleCanBeRemoved(ctx, organization, memberRoles...); err != nil {
		return nil, err
	}

	if err := o.deleteMember(ctx, organization, member); err != nil {
		return nil, err
	}

	activeID, err := o.GetActiveOrganizationID(ctx, session)
	if err != nil {
		return nil, err
	}

	sv := o.sessionOrganizationID(organization)
	if activeID != sv.Internal && activeID != sv.Client {
		return nil, nil
	}

	return o.SetActiveOrganization(ctx, session, nil)
}

func (o *organizationPlugin) GetMemberRoles(ctx context.Context, memberID any) ([]*access.Role, error) {
	roles, err := o.core.FindMany(ctx, o.memberRoleSchema, []limen.Where{
		limen.Eq(o.memberRoleSchema.GetMemberIDField(), memberID),
	})
	if err != nil {
		return nil, err
	}

	memberRoles := limen.MapToSliceOfType[*MemberRole](roles)
	dynamicRolesByID, err := o.resolveDynamicRolesByID(ctx, memberRoles)
	if err != nil {
		return nil, err
	}
	return o.composeMemberRoles(memberRoles, dynamicRolesByID)
}

// Get a complete organization member with all its related entities
func (o *organizationPlugin) GetMemberWithRelations(ctx context.Context, user *limen.User, organizationID any) (*Member, error) {
	organization, err := o.GetOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	member, err := o.GetMemberByUserID(ctx, organization.ID, user.ID)
	if err != nil {
		return nil, err
	}

	roles, err := o.GetMemberRoles(ctx, member.ID)
	if err != nil {
		return nil, err
	}

	member.Roles = roles
	member.User = user
	member.Organization = organization
	return member, nil
}

func (o *organizationPlugin) ListMembers(ctx context.Context, user *limen.User, organizationID any, opts *limen.QueryOptions) (*limen.Page[*Member], error) {
	organization, err := o.GetOrganization(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	if err := o.HasPermission(ctx, user, organization.ID, perms("member:read")); err != nil {
		return nil, err
	}
	members, err := o.core.FindWithOptions(ctx, o.memberSchema, []limen.Where{
		limen.Eq(o.memberSchema.GetOrganizationIDField(), organization.ID),
	}, opts)
	if err != nil {
		return nil, err
	}
	return limen.MapPage[*Member](members), nil
}

func (o *organizationPlugin) ListMembersWithRelations(ctx context.Context, user *limen.User, organizationID any, opts *limen.QueryOptions) (*limen.Page[*Member], error) {
	page, err := o.ListMembers(ctx, user, organizationID, opts)
	if err != nil {
		return nil, err
	}
	if err := o.attachMemberRelations(ctx, page.Items); err != nil {
		return nil, err
	}
	return page, nil
}

func (o *organizationPlugin) attachMemberRelations(ctx context.Context, members []*Member) error {
	if len(members) == 0 {
		return nil
	}
	if err := o.attachMemberRoles(ctx, members); err != nil {
		return err
	}
	return o.attachMemberUsers(ctx, members)
}

func (o *organizationPlugin) attachMemberRoles(ctx context.Context, members []*Member) error {
	memberIDs := make([]any, len(members))
	for i, member := range members {
		memberIDs[i] = member.ID
	}

	roles, err := o.core.FindMany(ctx, o.memberRoleSchema, []limen.Where{
		limen.In(o.memberRoleSchema.GetMemberIDField(), memberIDs),
	})
	if err != nil {
		return err
	}

	memberRoles := limen.MapToSliceOfType[*MemberRole](roles)
	rolesByMember := make(map[any][]*MemberRole, len(members))
	for _, memberRole := range memberRoles {
		rolesByMember[memberRole.MemberID] = append(rolesByMember[memberRole.MemberID], memberRole)
	}

	dynamicRolesByID, err := o.resolveDynamicRolesByID(ctx, memberRoles)
	if err != nil {
		return err
	}

	for _, member := range members {
		composed, err := o.composeMemberRoles(rolesByMember[member.ID], dynamicRolesByID)
		if err != nil {
			return err
		}
		member.Roles = composed
	}
	return nil
}

func (o *organizationPlugin) attachMemberUsers(ctx context.Context, members []*Member) error {
	userIDs := make([]any, len(members))
	for i, member := range members {
		userIDs[i] = member.UserID
	}

	users, err := o.core.FindMany(ctx, o.core.Schema.User, []limen.Where{
		limen.In(o.core.Schema.User.GetIDField(), userIDs),
	})
	if err != nil {
		return err
	}

	usersByID := make(map[any]*limen.User, len(users))
	for _, user := range limen.MapToSliceOfType[*limen.User](users) {
		usersByID[user.ID] = user
	}

	for _, member := range members {
		member.User = usersByID[member.UserID]
	}
	return nil
}

func (o *organizationPlugin) assignMemberRole(ctx context.Context, member *Member, role *access.Role) error {
	conditions := []limen.Where{
		limen.Eq(o.memberRoleSchema.GetMemberIDField(), member.ID),
		limen.Eq(o.memberRoleSchema.GetRoleField(), role.Name()),
	}

	if o.config.customRolesEnabled && role.ID() != nil {
		conditions = append(conditions, limen.Eq(o.memberRoleSchema.GetOrganizationRoleIDField(), role.ID()).Or())
	}

	existing, err := o.core.Exists(ctx, o.memberRoleSchema, conditions)
	if err != nil {
		return err
	}
	if existing {
		return ErrMemberRoleAlreadyExists
	}

	roleName := role.Name()
	if err := o.core.Create(ctx, o.memberRoleSchema, &MemberRole{
		OrganizationID:     member.OrganizationID,
		MemberID:           member.ID,
		Role:               &roleName,
		OrganizationRoleID: role.ID(),
	}, nil); err != nil {
		return err
	}

	return nil
}

func (o *organizationPlugin) memberRoleConditions(member *Member, role *access.Role) []limen.Where {
	conditions := []limen.Where{
		limen.Eq(o.memberRoleSchema.GetMemberIDField(), member.ID),
	}
	if role.ID() != nil {
		return append(conditions, limen.Eq(o.memberRoleSchema.GetOrganizationRoleIDField(), role.ID()))
	}
	return append(conditions, limen.Eq(o.memberRoleSchema.GetRoleField(), role.Name()))
}

func (o *organizationPlugin) checkMemberHasRole(ctx context.Context, member *Member, role *access.Role) error {
	held, err := o.core.Exists(ctx, o.memberRoleSchema, o.memberRoleConditions(member, role))
	if err != nil {
		return err
	}

	if !held {
		return ErrMemberRoleNotAssigned
	}
	return nil
}

func (o *organizationPlugin) validateMemberBelongsToOrganization(member *Member, organization *Organization) error {
	if member.OrganizationID != organization.ID {
		return ErrMemberNotInOrganization
	}
	return nil
}

func (o *organizationPlugin) validateOwnerRoleCanBeRemoved(ctx context.Context, organization *Organization, roles ...*access.Role) error {
	hasOwnerRole := slices.ContainsFunc(roles, o.isOwnerRole)

	if !hasOwnerRole {
		return nil
	}

	ownersCount, err := o.core.Count(ctx, o.memberRoleSchema, []limen.Where{
		limen.Eq(o.memberRoleSchema.GetOrganizationIDField(), organization.ID),
		limen.Eq(o.memberRoleSchema.GetRoleField(), o.getOwnerRole().Name()),
	})
	if err != nil {
		return err
	}

	if ownersCount == 1 {
		return ErrCannotRemoveLastOwner
	}
	return nil
}

func (o *organizationPlugin) checkMemberCanJoin(ctx context.Context, organization *Organization, user *limen.User) error {
	existing, err := o.core.Exists(ctx, o.memberSchema, []limen.Where{
		limen.Eq(o.memberSchema.GetOrganizationIDField(), organization.ID),
		limen.Eq(o.memberSchema.GetUserIDField(), user.ID),
	})
	if err != nil {
		return err
	}

	if existing {
		return ErrMemberAlreadyExists
	}

	if err := o.hasUserReachedMaxOrganizations(ctx, user); err != nil {
		return err
	}

	return o.checkOrganizationMemberLimit(ctx, organization)
}

// insertMemberWithRole writes the membership and its role. Callers own the guards
// and the hooks so they can place them relative to their own transaction.
func (o *organizationPlugin) insertMemberWithRole(ctx context.Context, organization *Organization, user *limen.User, role any) (*Member, error) {
	resolvedRoles, err := o.resolveRoles(ctx, organization, []any{role})
	if err != nil {
		return nil, err
	}

	var member *Member
	if err := o.core.WithTransaction(ctx, func(ctx context.Context) error {
		memberModel, err := o.core.CreateAndReturn(ctx, o.memberSchema, &Member{
			OrganizationID: organization.ID,
			UserID:         user.ID,
		}, nil, MemberSchemaOrganizationIDField, MemberSchemaUserIDField)
		if err != nil {
			return err
		}

		member = memberModel.(*Member)
		return o.assignMemberRole(ctx, member, resolvedRoles[0])
	}); err != nil {
		return nil, err
	}

	return member, nil
}

func (o *organizationPlugin) deleteMember(ctx context.Context, organization *Organization, member *Member) error {
	return o.core.WithTransaction(ctx, func(ctx context.Context) error {
		if err := o.core.Delete(ctx, o.memberRoleSchema, []limen.Where{
			limen.Eq(o.memberRoleSchema.GetMemberIDField(), member.ID),
			limen.Eq(o.memberRoleSchema.GetOrganizationIDField(), organization.ID),
		}); err != nil {
			return err
		}

		if err := o.core.Delete(ctx, o.memberSchema, []limen.Where{
			limen.Eq(o.memberSchema.GetOrganizationIDField(), organization.ID),
			limen.Eq(o.memberSchema.GetIDField(), member.ID),
		}); err != nil {
			return err
		}

		return o.clearActiveOrganizationFromSessions(ctx, organization.ID, map[limen.SchemaField]any{
			limen.SessionSchemaUserIDField: member.UserID,
		})
	})
}

// ListAccountMemberships reads the account's own organizations. It grants no
// access to other members and never changes the session's active organization.
func (o *organizationPlugin) ListAccountMemberships(ctx context.Context, user *limen.User, filter *ListOrganizationsFilter, opts *limen.QueryOptions) (*limen.Page[*Member], error) {
	organizations, err := o.ListOrganizations(ctx, user, filter, opts)
	if err != nil {
		return nil, err
	}
	result := &limen.Page[*Member]{Items: []*Member{}, Total: organizations.Total, Page: organizations.Page, PerPage: organizations.PerPage, TotalPages: organizations.TotalPages}
	if len(organizations.Items) == 0 {
		return result, nil
	}
	ids := make([]any, len(organizations.Items))
	for i, org := range organizations.Items {
		ids[i] = org.ID
	}
	rows, err := o.core.FindMany(ctx, o.memberSchema, []limen.Where{
		limen.Eq(o.memberSchema.GetUserIDField(), user.ID), limen.In(o.memberSchema.GetOrganizationIDField(), ids),
	})
	if err != nil {
		return nil, err
	}
	members := limen.MapToSliceOfType[*Member](rows)
	if err := o.attachMemberRoles(ctx, members); err != nil {
		return nil, err
	}
	byOrganization := make(map[any]*Member, len(members))
	for _, member := range members {
		byOrganization[member.OrganizationID] = member
	}
	for _, org := range organizations.Items {
		member, ok := byOrganization[org.ID]
		if !ok {
			return nil, ErrMemberNotInOrganization
		}
		member.Organization = org
		member.User = user
		result.Items = append(result.Items, member)
	}
	return result, nil
}
