//go:build !noembed

package beaverdream

import (
	"embed"
	"io/fs"

	"github.com/Napam/beaverdream/lib/hashfs"
	"github.com/go-chi/chi/v5"
)

//go:embed web/static/*
var webFS embed.FS

//go:embed db/world/migrations/*
var worldMigrationsFS embed.FS

//go:embed db/registry/migrations/*
var registryMigrationsFS embed.FS

var (
	// WorldMigrationsFS is the migration set every world file runs.
	WorldMigrationsFS fs.FS
	// RegistryMigrationsFS is the migration set for the registry file. The
	// world and registry sets are never mixed.
	RegistryMigrationsFS fs.FS
	hashFS               *hashfs.FS
)

func init() {
	stripped, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("failed to strip prefix: " + err.Error())
	}
	hashFS = hashfs.NewFS(stripped)

	WorldMigrationsFS, err = fs.Sub(worldMigrationsFS, "db/world/migrations")
	if err != nil {
		panic("failed to strip world migrations prefix: " + err.Error())
	}

	RegistryMigrationsFS, err = fs.Sub(registryMigrationsFS, "db/registry/migrations")
	if err != nil {
		panic("failed to strip registry migrations prefix: " + err.Error())
	}
}

// StaticRootPath returns a root-relative path with content hash for cache busting.
func StaticRootPath(path string) string {
	return "/" + hashFS.HashName(path)
}

// SetupStatic registers the static file server on the router.
func SetupStatic(r chi.Router) {
	r.Handle("/static/*", hashfs.FileServer(hashFS))
}
