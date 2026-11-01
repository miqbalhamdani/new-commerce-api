package team

// Themes are the hosted website's themes and each one's four brand colours,
// its default first (BR-143). Keep in step with ShopTheme in the contracts and
// the CHECK on storefront_settings.theme.
var Themes = map[string][4]string{
	"busana":          {"#6e4b3a", "#3f5a4c", "#7a3e52", "#26364f"},
	"clothing":        {"#2d5b8a", "#121212", "#4e5b31", "#a4472a"},
	"woman":           {"#8c6e57", "#141414", "#6e2f3c", "#55624a"},
	"katalog":         {"#2f5a4c", "#9a4e3a", "#6a3e5e", "#2d4a6e"},
	"katalog-hangat":  {"#4a2318", "#2f4a3a", "#3d2f5a", "#8a4b22"},
	"furnitur":        {"#8a5a3b", "#5e6b4e", "#34505f", "#1e1c1a"},
	"furnitur-earthy": {"#8b4426", "#5f6347", "#1c1a15", "#9a6b3f"},
	"furnitur-ceria":  {"#0b55c4", "#14171f", "#0e7c66", "#c8442b"},
}

// DefaultTheme is a new shop's theme, and the one a shop with no settings row
// has.
const DefaultTheme = "katalog"

// ResolveColor is the colour a website draws a shop in: the one picked, or the
// theme's first when none is (BR-142).
func ResolveColor(theme string, color *string) string {
	if color != nil {
		return *color
	}
	if c, ok := Themes[theme]; ok {
		return c[0]
	}
	return Themes[DefaultTheme][0]
}
