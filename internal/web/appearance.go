package web

import "net/http"

// Theme names a built-in look. style.css sets its colors and fonts under
// [data-theme="…"], and ui.js keeps the choice per browser.
type Theme string

const (
	ThemeDefault Theme = "default"
	ThemeSepia   Theme = "sepia"
)

type themeOption struct {
	Theme       Theme
	Name        string
	Description string
}

func (o themeOption) IsDefault() bool { return o.Theme == ThemeDefault }

// themes lists the choices on the appearance page, the default first.
var themes = []themeOption{
	{ThemeDefault, "Default", "Your system's fonts on white, or on dark grey in dark mode."},
	{ThemeSepia, "Sepia", "Lora for text and Fira Code for code, on warm paper colors or dark brown in dark mode."},
}

type appearanceData struct {
	Themes []themeOption
}

// handleAppearance shows the theme choices. The choice stays in the
// browser, so the page sends nothing back.
func (s *Server) handleAppearance(w http.ResponseWriter, r *http.Request) {
	sidebar, err := s.sidebarData(r.Context(), listQuery{})
	if err != nil {
		s.fail(w, err)
		return
	}
	sidebar.Appearance = true
	s.render(w, part{"layout", pageData{Title: "Appearance", Sidebar: sidebar, Appearance: &appearanceData{Themes: themes}}})
}
