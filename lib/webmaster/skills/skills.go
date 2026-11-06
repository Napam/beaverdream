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
		Summary: "how a world someone moves through should feel: keys, cameras, genres, and a tested platformer core",
		Body:    controlsReference,
	},
	{
		Name:    "feel",
		Summary: "make actions feel physical: shake, hit stop, particles, squash, generated sound and rumble from one impact call",
		Body:    feelReference,
	},
	{
		Name:    "space",
		Summary: "a grid world that can be broken or dug: collision, ray casts, line of sight, area damage and a spatial hash",
		Body:    spaceReference,
	},
	{
		Name:    "entities",
		Summary: "several kinds of characters or enemies from tables: moves per kind, charge, cooldowns, transforms, shots, spawning",
		Body:    entitiesReference,
	},
	{
		Name:    "paths",
		Summary: "find the way around walls: A* for a few seekers, one flow field for a crowd, path following and spreading apart",
		Body:    pathsReference,
	},
	{
		Name:    "random",
		Summary: "seeded randomness everyone agrees on: daily seeds, forks, noise terrain, mazes, dungeons, caves and even scatter",
		Body:    randomReference,
	},
	{
		Name:    "canvas",
		Summary: "sharp, cheap 2D drawing: pixel ratio, a camera with mouse to world, cached autotiled tile maps, sprites from text, parallax, y sorting, light",
		Body:    canvasReference,
	},
	{
		Name:    "characters",
		Summary: "3D figures that look alive: body plans (biped, quadruped, bug, flyer, blob, vehicle), a walk, run, jump and land from movement, seeded looks, moves; the same gait for 2D",
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
