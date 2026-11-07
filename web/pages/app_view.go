package pages

import "time"

// AppWorld is one world in the lobby or the admin list.
type AppWorld struct {
	ID        string
	Slug      string
	Status    string
	CreatedAt time.Time
}

// AppInviteRow is one invite row. The raw code is never stored, so it is absent.
type AppInviteRow struct {
	ID        string
	Label     string
	MaxUses   int
	Uses      int
	ExpiresAt *time.Time
	Revoked   bool
}

// AppIdentity is one account in the admin list.
type AppIdentity struct {
	ID          string
	DisplayName string
	Disabled    bool
}

// AppView is everything the app's own pages show.
type AppView struct {
	// Name is the signed-in identity's display name.
	Name  string
	Admin bool
	CSRF  string
	// Error is a refusal message. Every invalid invite shares the same one.
	Error string
	// Next is where a successful login returns.
	Next string
	// InviteCode is the code named by the redemption page, never shown after
	// the form is submitted.
	InviteCode string

	Worlds     []AppWorld
	Invites    []AppInviteRow
	Identities []AppIdentity

	// NewInviteLink is the one-time redemption URL after creating an invite.
	NewInviteLink string
	// NewAPIToken is the one-time token after minting one.
	NewAPIToken string
}

// WorldPlayPath is the app route that mints a handoff for a world.
func WorldPlayPath(slug string) string {
	return "/w/" + slug + "/play"
}

// InvitePath is the public redemption page.
func InvitePath(code string) string {
	return "/invite/" + code
}
