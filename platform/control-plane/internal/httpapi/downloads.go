package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/go-chi/chi/v5"
)

// downloadName is what a file in the downloads folder may be called: the client builds of a
// release and manifest.json, never a path.
var downloadName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// download serves the client builds setup put in the downloads folder (DOWNLOADS_DIR) and their
// manifest.json, which the WebUI's home page reads. They are the release's public files, so no
// sign-in is needed. A folder without them answers 404: the home page then links to the release.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.config.DownloadsDir == "" || !downloadName.MatchString(name) {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	path := filepath.Join(s.config.DownloadsDir, name)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	if name == "manifest.json" {
		// Setup rewrites it on every upgrade; the home page must see the new version at once.
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeFile(w, r, path)
}
