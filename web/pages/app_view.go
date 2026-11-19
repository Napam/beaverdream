package pages

import (
	"time"

	"github.com/Napam/beaverdream/lib/webmaster"
)

// AppWorld is one world in the lobby or the admin list.
type AppWorld struct {
	ID        string
	Slug      string
	Status    string
	CreatedAt time.Time
	// WorldURL is the world's page on the play port, seen from the app.
	WorldURL        string
	CreatorName     string
	CreatorUsername string
	// Health is the registry's cached view of the world, so the gallery can
	// show a broken or stale world without opening its file. HealthKnown is
	// false before a row exists.
	HealthKnown bool
	LastTurnAt  time.Time
	LastStatus  string
	PageBytes   int
	Alerts      int
}

// HealthState is what the card shows at a glance: error, alerts, ok, or none
// while the registry has no row yet.
func (w AppWorld) HealthState() string {
	switch {
	case w.LastStatus == "error":
		return "error"
	case w.Alerts > 0:
		return "alerts"
	case !w.HealthKnown && w.LastStatus == "":
		return "none"
	}
	return "ok"
}

// HealthRank orders the gallery: broken worlds first, then open alerts, then
// everything else.
func (w AppWorld) HealthRank() int {
	switch w.HealthState() {
	case "error":
		return 0
	case "alerts":
		return 1
	}
	return 2
}

// PagePercent is the current page against the model's page cap, for the card's
// size bar.
func (w AppWorld) PagePercent() int {
	limit := webmaster.PageBytes()
	if limit <= 0 {
		return 0
	}
	percent := w.PageBytes * 100 / limit
	if percent > 100 {
		return 100
	}
	if percent < 0 {
		return 0
	}
	return percent
}

// PageNearlyFull reports a page at or over 90% of its cap.
func (w AppWorld) PageNearlyFull() bool {
	return w.PagePercent() >= 90
}

// LastTurnLine is the card's one muted line about the last turn.
func (w AppWorld) LastTurnLine() string {
	if w.LastTurnAt.IsZero() {
		return "never played"
	}
	return "last turn " + shortAge(time.Since(w.LastTurnAt)) + " · " +
		healthStatusWord(w.LastStatus)
}

// shortAge names a span the way a card reads it: minutes, then hours, then
// days.
func shortAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return itoa(int(age/time.Minute)) + "m ago"
	case age < 24*time.Hour:
		return itoa(int(age/time.Hour)) + "h ago"
	}
	return itoa(int(age/(24*time.Hour))) + "d ago"
}

// healthStatusWord turns a turn status into the word the card shows. A world
// whose newest turn is a server event, or which never ran a changeset, reads
// as idle: its page was not touched by the model.
func healthStatusWord(status string) string {
	switch status {
	case "changed", "kept", "silent", "alert":
		return status
	case "error":
		return "failed"
	}
	return "idle"
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
	Username    string
	Disabled    bool
	// Admin reports an active ADMIN role.
	Admin bool
	// SystemAdmin is the fixed zero-id account. It cannot be disabled or
	// demoted.
	SystemAdmin bool
	CreatedAt   time.Time
}

// The app admin's sections. The URL picks one, so a reload and a bookmark
// land on the same page instead of the top of one long scroll.
const (
	AdminWorlds   = "worlds"
	AdminInvites  = "invites"
	AdminAccounts = "accounts"
	AdminModel    = "model"
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
	Error               string
	Notice              string
	Username            string
	PasswordEditable    bool
	PasswordSystemAdmin bool
	PasswordNote        string
	PasswordError       string
	NewWorldSlug        string
	CreatedSlug         string
	WorldCreationOpen   bool
	WorldCreationKnown  bool
	// AdminSection is which app admin page is showing: worlds, invites or
	// accounts. Empty on every other page.
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
	// NewPasswordResetLink is the one-time reset URL after issuing one.
	NewPasswordResetLink string
	// NewPasswordResetFor names the account the link belongs to.
	NewPasswordResetFor string
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

// ResetPath is the public password reset page.
func ResetPath(code string) string {
	return "/reset/" + code
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
