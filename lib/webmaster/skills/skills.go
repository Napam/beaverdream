// Package skills holds the how-tos the model reads on demand with
// load_skill. A body comes back as a tool result, so it stays out of the room
// log, the page and the system prompt. Each body holds one script block that
// its *_test.go runs.
package skills

import "strings"

// Skill is one how-to: its name, the one-line summary in the tool
// description, and the body load_skill returns.
type Skill struct {
	Name    string
	Summary string
	Body    string
}

// All lists the skills in the order the tool describes them.
var All = []Skill{
	{
		Name:    "controls",
		Summary: "for movement: keys, cameras, genre defaults and a tested platformer core",
		Body:    controlsReference,
	},
	{
		Name:    "feel",
		Summary: "for actions and impacts: shake, hit stop, particles, squash, sound and rumble",
		Body:    feelReference,
	},
	{
		Name:    "space",
		Summary: "for walls, digging and destruction: grid collision, rays, line of sight, area damage and a spatial hash",
		Body:    spaceReference,
	},
	{
		Name:    "entities",
		Summary: "for character kinds or reactive enemies: moves, charge, cooldowns, transforms, shots and spawning",
		Body:    entitiesReference,
	},
	{
		Name:    "paths",
		Summary: "for finding a way around walls: A*, crowd flow fields, path following and spacing",
		Body:    pathsReference,
	},
	{
		Name:    "random",
		Summary: "for generated worlds, puzzles, decks or art: shared seeds, noise, mazes, dungeons, caves and scatter",
		Body:    randomReference,
	},
	{
		Name:    "canvas",
		Summary: "for 2D canvas drawing: pixel ratio, cameras, cached tiles, sprites, parallax, sorting and light",
		Body:    canvasReference,
	},
	{
		Name:    "characters",
		Summary: "for 3D figures or drawn 2D gaits: body plans, movement-driven animation, seeded looks and actions",
		Body:    charactersReference,
	},
}

// Body returns the body of the named skill.
func Body(name string) (string, bool) {
	for _, s := range All {
		if s.Name == name {
			return s.Body, true
		}
	}
	return "", false
}

// Names lists the skill names in registry order, for the tool's enum.
func Names() []string {
	names := make([]string, len(All))
	for i, s := range All {
		names[i] = s.Name
	}
	return names
}

// Summaries lists each skill for the load_skill description.
func Summaries() string {
	lines := make([]string, len(All))
	for i, s := range All {
		lines[i] = s.Name + " (" + s.Summary + ")"
	}
	return strings.Join(lines, "; ")
}
