package pages

import "time"

// AppWorld is one world in the lobby or the admin list.
type AppWorld struct {
	ID        string
	Slug      string
	Status    string
	CreatedAt time.Time
	// BoardURL is the world's page on the play port, seen from the app.
	BoardURL string
}

// AppInviteRow is one invite row. The raw code is never stored, so it is absent.
type AppInviteRow struct {
	ID        string
	Label     string
	MaxUses   int
	Uses      int
	ExpiresAt *time.Time
	Revoked   bool
	CreatedAt time.Time
}

// Status names an invite's state for the admin list.
func (i AppInviteRow) Status() string {
	switch {
	case i.Revoked:
		return "revoked"
	case i.ExpiresAt != nil && i.ExpiresAt.Before(time.Now()):
		return "expired"
	case i.Uses >= i.MaxUses:
		return "used up"
	}
	return "open"
}

// AppIdentity is one account in the admin list.
type AppIdentity struct {
	ID          string
	DisplayName string
	Disabled    bool
	CreatedAt   time.Time
}

// The app admin's sections. The URL picks one, so a reload and a bookmark
// land on the same page instead of the top of one long scroll.
const (
	AdminWorlds  = "worlds"
	AdminInvites = "invites"
	AdminPeople  = "people"
	AdminModel   = "model"
)

// AppSkill is one registered skill, with the body load_skill returns.
type AppSkill struct {
	Name    string
	Summary string
	Body    string
}

// AppTool is one tool the webmaster can call.
type AppTool struct {
	Name        string
	Description string
	Parameters  string
}

// AppView is everything the app's own pages show.
type AppView struct {
	// Name is the signed-in identity's display name.
	Name  string
	Admin bool
	CSRF  string
	// Error is a refusal message. Every invalid invite shares the same one.
	Error string
	// AdminSection is which app admin page is showing: worlds, invites or
	// people. Empty on every other page.
	AdminSection string
	// Next is where a successful login returns.
	Next string
	// InviteCode is the code named by the redemption page, never shown after
	// the form is submitted.
	InviteCode string

	Worlds     []AppWorld
	Invites    []AppInviteRow
	Identities []AppIdentity
	Skills     []AppSkill
	Tools      []AppTool

	// NewInviteLink is the one-time redemption URL after creating an invite.
	NewInviteLink string
	// NewAPIToken is the one-time token after minting one.
	NewAPIToken string
}

// WorldPlayPath is the app route that mints a handoff for a world.
func WorldPlayPath(slug string) string {
	return "/worlds/" + slug + "/play"
}

// InvitePath is the public redemption page.
func InvitePath(code string) string {
	return "/invite/" + code
}

// appAdminTitle names the instance admin page in the browser tab: the visible
// section, then the surface. World admin pages have their own title.
func appAdminTitle(view AppView) string {
	section := view.AdminSection
	if section == "" {
		section = AdminWorlds
	}
	return section + " · back office · beaverdream"
}
