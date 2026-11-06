package webmaster

import "strings"

// skill is a how-to the model reads on demand with load_skill. The body comes
// back as a tool result, so it stays out of the room log, the page and the
// system prompt.
type skill struct {
	name    string
	summary string
	body    string
}

var skills = []skill{
	{
		name:    "controls",
		summary: "how a world someone moves through should feel: keys, cameras, genres, and a tested platformer core",
		body:    controlsReference,
	},
	{
		name:    "feel",
		summary: "make actions feel physical: shake, hit stop, particles, squash, generated sound and rumble from one impact call",
		body:    feelReference,
	},
	{
		name:    "space",
		summary: "a grid world that can be broken or dug: collision, ray casts, line of sight, area damage and a spatial hash",
		body:    spaceReference,
	},
	{
		name:    "entities",
		summary: "several kinds of characters or enemies from tables: moves per kind, charge, cooldowns, transforms, shots, spawning",
		body:    entitiesReference,
	},
	{
		name:    "paths",
		summary: "find the way around walls: A* for a few seekers, one flow field for a crowd, path following and spreading apart",
		body:    pathsReference,
	},
}

// skillBody returns the body of the named skill.
func skillBody(name string) (string, bool) {
	for _, s := range skills {
		if s.name == name {
			return s.body, true
		}
	}
	return "", false
}

// skillNames lists the skill names in registry order, for the tool's enum.
func skillNames() []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.name
	}
	return names
}

// skillSummaries lists each skill for the load_skill description.
func skillSummaries() string {
	lines := make([]string, len(skills))
	for i, s := range skills {
		lines[i] = s.name + " (" + s.summary + ")"
	}
	return strings.Join(lines, "; ")
}
