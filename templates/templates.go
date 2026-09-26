// Package templates embeds the HTML templates for the HostGlance pages.
//
// Layout: base.html defines the page shell, partials/ hold shared blocks
// (design tokens, topbar, shared JS), and pages/ hold one file per page
// overriding the base blocks (title, styles, content, scripts).
package templates

import (
	"embed"
	"fmt"
	"html/template"
)

//go:embed base.html partials/*.html pages/*.html
var fs embed.FS

// pageFiles maps page names to their template file.
var pageFiles = map[string]string{
	"dashboard": "pages/dashboard.html",
	"history":   "pages/history.html",
	"system":    "pages/system.html",
}

// Pages parses one template set per page; every set shares base.html and the
// partials so page files can override base blocks without name collisions.
// Render with ExecuteTemplate(w, "base", data).
func Pages(funcs template.FuncMap) (map[string]*template.Template, error) {
	out := make(map[string]*template.Template, len(pageFiles))
	for name, file := range pageFiles {
		t, err := template.New(name).Funcs(funcs).ParseFS(fs, "base.html", "partials/*.html", file)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}
