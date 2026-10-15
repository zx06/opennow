package auth

func SiteAuthCookieName(slug string) string {
	return "opennow_site_" + slug
}
