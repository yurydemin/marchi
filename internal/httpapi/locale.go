package httpapi

import (
	"html/template"
	"net/url"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/text/language"

	"github.com/yurydemin/marchi/internal/i18n"
	"github.com/yurydemin/marchi/internal/webui"
)

// langCookieName mirrors app.js's own "theme" cookie: a plain,
// long-lived, client-readable preference, not session state — language
// (unlike the unlock session) has nothing to protect, so it's set
// directly by /lang/:code rather than living behind a signed session.
const langCookieName = "lang"

// langCookieMaxAge is one year, matching web/static/js/app.js's theme
// cookie.
const langCookieMaxAge = 365 * 24 * 60 * 60

// supportedTags mirrors i18n.Supported index-for-index — langMatcher's
// Match returns an index into whichever slice of tags it was built from,
// which resolveLang uses to index back into i18n.Supported.
var supportedTags = []language.Tag{language.English, language.Russian}
var langMatcher = language.NewMatcher(supportedTags)

// localeMiddleware resolves the request's UI language exactly once
// (cookie override, else Accept-Language, else i18n.Default) and stashes
// it in Locals so every handler downstream builds the same *i18n.Localizer
// via localizer(c) without re-parsing the request each time.
func localeMiddleware(c *fiber.Ctx) error {
	c.Locals("lang", resolveLang(c))
	return c.Next()
}

func resolveLang(c *fiber.Ctx) string {
	if v := c.Cookies(langCookieName); v != "" {
		for _, s := range i18n.Supported {
			if s == v {
				return v
			}
		}
	}
	if header := c.Get(fiber.HeaderAcceptLanguage); header != "" {
		if tags, _, err := language.ParseAcceptLanguage(header); err == nil && len(tags) > 0 {
			_, idx, _ := langMatcher.Match(tags...)
			return i18n.Supported[idx]
		}
	}
	return i18n.Default
}

// localizer builds this request's *i18n.Localizer from the language
// localeMiddleware already resolved.
func localizer(c *fiber.Ctx) *i18n.Localizer {
	lang, _ := c.Locals("lang").(string)
	return i18n.NewLocalizer(lang)
}

// render replaces every page handler's own
// pages[page].ExecuteTemplate(c, name, data) with a call that first binds
// "T"/"conditionTypeLabel" to this request's resolved language (see
// webui.Bind's doc comment for why that has to happen per request rather
// than once at Parse time).
func render(c *fiber.Ctx, pages map[string]*template.Template, page, name string, data any) error {
	tpl, err := webui.Bind(pages[page], localizer(c))
	if err != nil {
		return err
	}
	return tpl.ExecuteTemplate(c, name, data)
}

// registerLangSwitch wires GET /lang/:code (layout.html's language
// switcher): sets the cookie and redirects back to wherever the request
// came from. A plain GET, not a form POST, matches the theme toggle's own
// "harmless client preference" trust level and keeps the switcher a
// same-origin-only <a href> — an off-site Referer is never followed
// (redirectTarget falls back to "/" instead), so this can't be used as an
// open redirect.
func registerLangSwitch(app *fiber.App) {
	app.Get("/lang/:code", func(c *fiber.Ctx) error {
		code := c.Params("code")
		for _, s := range i18n.Supported {
			if s == code {
				c.Cookie(&fiber.Cookie{
					Name:     langCookieName,
					Value:    code,
					MaxAge:   langCookieMaxAge,
					Path:     "/",
					SameSite: "Lax",
				})
				break
			}
		}
		return c.Redirect(redirectTarget(c))
	})
}

// redirectTarget returns the same-origin path the language switcher
// should return to, from the Referer header — or "/" if that header is
// missing or points somewhere else entirely (a cross-origin Referer isn't
// trusted as a redirect target).
func redirectTarget(c *fiber.Ctx) string {
	ref := c.Get(fiber.HeaderReferer)
	if ref == "" {
		return "/"
	}
	u, err := url.Parse(ref)
	if err != nil || u.Hostname() != c.Hostname() {
		return "/"
	}
	if u.Path == "" {
		return "/"
	}
	target := u.Path
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}
