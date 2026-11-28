package main

import "net/http"

// Only known page routes serve the application shell. Unknown paths keep 404.
var sectionPaths = []string{
	"/requests", "/tickets", "/analytics", "/optimization", "/administration", "/notifications", "/tasks", "/analytics/processes", "/reports",
	"/settings/workflows", "/settings/assets", "/settings/governance", "/settings/structure", "/settings/organization", "/settings/knowledge", "/settings/api-check", "/settings/1c",
	"/settings/connections", "/settings/communication", "/settings/import", "/settings/preview", "/settings/widget", "/settings/api",
}

func registerSectionRoutes(mux *http.ServeMux) {
	for _, path := range sectionPaths {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, "web/index.html")
		})
	}
}
