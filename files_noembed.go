//go:build noembed

package beaverdream

import (
	"io/fs"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
)

var (
	// WorldMigrationsFS is the migration set every world file runs.
	WorldMigrationsFS fs.FS
	// RegistryMigrationsFS is the migration set for the registry file.
	RegistryMigrationsFS fs.FS
)

func init() {
	WorldMigrationsFS = os.DirFS("db/world/migrations")
	RegistryMigrationsFS = os.DirFS("db/registry/migrations")
}

// StaticRootPath returns the path as-is (no content hashing in dev mode).
func StaticRootPath(path string) string {
	return "/" + path
}

// SetupStatic registers a file server with no-cache headers for development.
func SetupStatic(r chi.Router) {
	fileServer := http.FileServer(http.Dir("web/static"))
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.StripPrefix("/static/", fileServer).ServeHTTP(w, r)
	}))
}
