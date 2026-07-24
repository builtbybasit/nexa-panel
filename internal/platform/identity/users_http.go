package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/uptrace/bun"
)

// SiteDirectory validates site grants without importing the sites feature
// module; the concrete implementation is injected from cmd/nexa.
type SiteDirectory interface {
	SiteExists(ctx context.Context, id string) (bool, error)
}

func (m *Module) SetSiteDirectory(directory SiteDirectory) { m.siteDirectory = directory }

var (
	errLastAdmin    = errors.New("the last administrator cannot be demoted or deleted")
	errNotDeveloper = errors.New("site grants apply to developer users only")
)

type siteGrantModel struct {
	bun.BaseModel `bun:"table:identity_site_grants,alias:identity_site_grant"`
	UserID        string    `bun:",pk"`
	SiteID        string    `bun:",pk"`
	CreatedAt     time.Time `bun:",notnull"`
}

type ManagedUser struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
	MFAConfirmed bool       `json:"mfaConfirmed"`
	SiteIDs      []string   `json:"siteIds"`
}

func (model userModel) toManagedUser(siteIDs []string) ManagedUser {
	if siteIDs == nil {
		siteIDs = []string{}
	}
	return ManagedUser{
		ID: model.ID, Username: model.Username, Role: model.Role, CreatedAt: model.CreatedAt,
		LastLoginAt: model.LastLoginAt, MFAConfirmed: model.TOTPConfirmedAt != nil, SiteIDs: siteIDs,
	}
}

func validManagedRole(role string) bool {
	switch role {
	case "admin", "operator", "developer", "viewer":
		return true
	}
	return false
}

// SiteAccessible reports whether the user may act on the site. Only the
// developer role is scoped by grants; every other role sees all sites.
func (m *Module) SiteAccessible(ctx context.Context, user User, siteID string) (bool, error) {
	if user.Role != "developer" {
		return true, nil
	}
	return m.database.NewSelect().Model((*siteGrantModel)(nil)).
		Where("user_id = ?", user.ID).Where("site_id = ?", siteID).Exists(ctx)
}

// AccessibleSiteIDs returns all=true for unscoped roles, otherwise the site
// IDs granted to the developer.
func (m *Module) AccessibleSiteIDs(ctx context.Context, user User) (bool, []string, error) {
	if user.Role != "developer" {
		return true, nil, nil
	}
	ids, err := m.grantedSiteIDs(ctx, user.ID)
	if err != nil {
		return false, nil, err
	}
	return false, ids, nil
}

func (m *Module) grantedSiteIDs(ctx context.Context, userID string) ([]string, error) {
	ids := make([]string, 0)
	err := m.database.NewSelect().Model((*siteGrantModel)(nil)).Column("site_id").
		Where("user_id = ?", userID).OrderExpr("site_id").Scan(ctx, &ids)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (m *Module) listUsersHTTP(w http.ResponseWriter, r *http.Request) {
	models := make([]userModel, 0)
	err := m.database.NewSelect().Model(&models).
		Column("id", "username", "role", "created_at", "last_login_at", "totp_confirmed_at").
		OrderExpr("username COLLATE NOCASE").Scan(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "Users could not be loaded.")
		return
	}
	grants := make([]siteGrantModel, 0)
	if err := m.database.NewSelect().Model(&grants).OrderExpr("site_id").Scan(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "Users could not be loaded.")
		return
	}
	siteIDs := make(map[string][]string, len(grants))
	for _, grant := range grants {
		siteIDs[grant.UserID] = append(siteIDs[grant.UserID], grant.SiteID)
	}
	items := make([]ManagedUser, 0, len(models))
	for index := range models {
		items = append(items, models[index].toManagedUser(siteIDs[models[index].ID]))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (m *Module) createUserHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	input, decodeErr := httpapi.Decode[createUserRequest](w, r)
	if decodeErr != nil {
		httpapi.Fail(w, decodeErr)
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if err := validateCredentials(credentials{Username: input.Username, Password: input.Password}); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_credentials", err.Error())
		return
	}
	if !validManagedRole(input.Role) {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_role", "Role must be admin, operator, developer, or viewer.")
		return
	}
	passwordHash, err := hashPassword(input.Password, m.parameters)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be created.")
		return
	}
	model := &userModel{
		ID: randomID(16), Username: input.Username, PasswordHash: passwordHash, Role: input.Role,
		CreatedAt: m.now().UTC(), RecoveryCodeHashes: "[]",
	}
	if _, err := m.database.NewInsert().Model(model).Exec(r.Context()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			httpapi.WriteError(w, http.StatusConflict, "username_taken", "A user with this username already exists.")
			return
		}
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be created.")
		return
	}
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &actor.ID, Action: "identity.user_created", Subject: "user:" + model.ID, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"username": model.Username, "role": model.Role}})
	httpapi.WriteJSON(w, http.StatusCreated, model.toManagedUser(nil))
}

type updateUserRequest struct {
	Role     *string `json:"role,omitempty"`
	Password *string `json:"password,omitempty"`
}

func (m *Module) updateUserHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	input, decodeErr := httpapi.Decode[updateUserRequest](w, r)
	if decodeErr != nil {
		httpapi.Fail(w, decodeErr)
		return
	}
	if input.Role == nil && input.Password == nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Provide a role or a password to update.")
		return
	}
	if input.Role != nil && !validManagedRole(*input.Role) {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_role", "Role must be admin, operator, developer, or viewer.")
		return
	}
	id := r.PathValue("id")
	passwordHash := ""
	if input.Password != nil {
		// The policy rejects a password containing the account's own username, so
		// the target's name is read up front; a missing row is reported the same
		// way the transaction below would report it.
		var username string
		if err := m.database.NewSelect().Model((*userModel)(nil)).Column("username").
			Where("id = ?", id).Scan(r.Context(), &username); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				httpapi.WriteError(w, http.StatusNotFound, "user_not_found", "The requested user does not exist.")
				return
			}
			httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be updated.")
			return
		}
		if err := validatePassword(*input.Password, username); err != nil {
			httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_credentials", err.Error())
			return
		}
		hash, err := hashPassword(*input.Password, m.parameters)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be updated.")
			return
		}
		passwordHash = hash
	}
	model := new(userModel)
	err := m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewSelect().Model(model).
			Column("id", "username", "role", "created_at", "last_login_at", "totp_confirmed_at").
			Where("id = ?", id).Scan(ctx); err != nil {
			return err
		}
		if input.Role != nil && model.Role == "admin" && *input.Role != "admin" {
			admins, err := tx.NewSelect().Model((*userModel)(nil)).Where("role = 'admin'").Count(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errLastAdmin
			}
		}
		update := tx.NewUpdate().Model((*userModel)(nil)).Where("id = ?", id)
		if input.Role != nil {
			update = update.Set("role = ?", *input.Role)
			model.Role = *input.Role
		}
		if input.Password != nil {
			update = update.Set("password_hash = ?", passwordHash)
		}
		if _, err := update.Exec(ctx); err != nil {
			return err
		}
		// Role and password changes invalidate every live session of the user.
		_, err := tx.NewDelete().Model((*sessionModel)(nil)).Where("user_id = ?", id).Exec(ctx)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "user_not_found", "The requested user does not exist.")
		return
	}
	if errors.Is(err, errLastAdmin) {
		httpapi.WriteError(w, http.StatusConflict, "last_admin", "The last administrator cannot lose the admin role.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be updated.")
		return
	}
	metadata := map[string]any{"passwordChanged": input.Password != nil}
	if input.Role != nil {
		metadata["role"] = *input.Role
	}
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &actor.ID, Action: "identity.user_updated", Subject: "user:" + id, RemoteAddress: remoteAddress(r), Metadata: metadata})
	siteIDs, err := m.grantedSiteIDs(r.Context(), id)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, model.toManagedUser(siteIDs))
}

func (m *Module) deleteUserHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	id := r.PathValue("id")
	if id == actor.ID {
		httpapi.WriteError(w, http.StatusConflict, "self_delete_forbidden", "You cannot delete your own account.")
		return
	}
	model := new(userModel)
	err := m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewSelect().Model(model).Column("id", "username", "role").Where("id = ?", id).Scan(ctx); err != nil {
			return err
		}
		if model.Role == "admin" {
			admins, err := tx.NewSelect().Model((*userModel)(nil)).Where("role = 'admin'").Count(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errLastAdmin
			}
		}
		if _, err := tx.NewDelete().Model((*siteGrantModel)(nil)).Where("user_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		// Sessions cascade through the identity_sessions foreign key.
		_, err := tx.NewDelete().Model((*userModel)(nil)).Where("id = ?", id).Exec(ctx)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "user_not_found", "The requested user does not exist.")
		return
	}
	if errors.Is(err, errLastAdmin) {
		httpapi.WriteError(w, http.StatusConflict, "last_admin", "The last administrator cannot be deleted.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "The user could not be deleted.")
		return
	}
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &actor.ID, Action: "identity.user_deleted", Subject: "user:" + id, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"username": model.Username, "role": model.Role}})
	w.WriteHeader(http.StatusNoContent)
}

type replaceSitesRequest struct {
	SiteIDs []string `json:"siteIds"`
}

func (m *Module) replaceUserSitesHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if m.siteDirectory == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "sites_unavailable", "Site grants are not configured on this control plane.")
		return
	}
	input, decodeErr := httpapi.Decode[replaceSitesRequest](w, r)
	if decodeErr != nil {
		httpapi.Fail(w, decodeErr)
		return
	}
	if input.SiteIDs == nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "siteIds must be provided.")
		return
	}
	siteIDs := dedupeStrings(input.SiteIDs)
	for _, siteID := range siteIDs {
		exists, err := m.siteDirectory.SiteExists(r.Context(), siteID)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "Site grants could not be validated.")
			return
		}
		if !exists {
			httpapi.WriteError(w, http.StatusUnprocessableEntity, "unknown_site", "Site "+siteID+" does not exist.")
			return
		}
	}
	id := r.PathValue("id")
	now := m.now().UTC()
	err := m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		model := new(userModel)
		if err := tx.NewSelect().Model(model).Column("id", "role").Where("id = ?", id).Scan(ctx); err != nil {
			return err
		}
		if model.Role != "developer" {
			return errNotDeveloper
		}
		if _, err := tx.NewDelete().Model((*siteGrantModel)(nil)).Where("user_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if len(siteIDs) == 0 {
			return nil
		}
		grants := make([]siteGrantModel, 0, len(siteIDs))
		for _, siteID := range siteIDs {
			grants = append(grants, siteGrantModel{UserID: id, SiteID: siteID, CreatedAt: now})
		}
		_, err := tx.NewInsert().Model(&grants).Exec(ctx)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "user_not_found", "The requested user does not exist.")
		return
	}
	if errors.Is(err, errNotDeveloper) {
		httpapi.WriteError(w, http.StatusBadRequest, "user_not_developer", "Site grants apply to developer users only.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "identity_unavailable", "Site grants could not be updated.")
		return
	}
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &actor.ID, Action: "identity.user_site_grants_replaced", Subject: "user:" + id, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"siteIds": siteIDs}})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"siteIds": siteIDs})
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
