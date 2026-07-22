// Package i18n resolves Marchi's UI language (ru/en, FR-WU-*) and looks up
// translated strings for both the Web UI (per-request) and the CLI
// (once, at process startup). Message catalogs live in locales/ as TOML,
// embedded into the binary like every other web asset (see web.Templates).
package i18n

import (
	"embed"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml
var localeFS embed.FS

// Default is the fallback language when no cookie, header, flag, or env
// var picks one — English, matching every other default in this project
// (log messages, error strings, the original UI before this step).
const Default = "en"

// Supported lists every language with a message catalog, in the order the
// language switcher (layout.html) presents them.
var Supported = []string{"en", "ru"}

var bundle = mustLoadBundle()

func mustLoadBundle() *goi18n.Bundle {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", tomlUnmarshal)
	for _, lang := range Supported {
		if _, err := b.LoadMessageFileFS(localeFS, "locales/active."+lang+".toml"); err != nil {
			panic("i18n: loading locales/active." + lang + ".toml: " + err.Error())
		}
	}
	return b
}

// Localizer looks up messages for one resolved language. Safe for
// concurrent use (go-i18n's own Localizer is), matching how a single
// *Localizer is built once per HTTP request and read from multiple
// template executions (layout + content) during that request.
type Localizer struct {
	loc  *goi18n.Localizer
	Lang string
}

// NewLocalizer resolves lang (which need not be exact — "ru-RU" matches
// the "ru" catalog) against Supported, falling back to Default if lang is
// empty or unrecognized entirely. go-i18n itself already falls back
// message-by-message to the bundle's default language (English) for any
// ID missing from a partially-translated catalog.
func NewLocalizer(lang string) *Localizer {
	resolved := Default
	for _, s := range Supported {
		if s == lang {
			resolved = lang
			break
		}
	}
	return &Localizer{loc: goi18n.NewLocalizer(bundle, resolved), Lang: resolved}
}

// TplData is the optional keyword-argument map passed to T for messages
// with placeholders (e.g. "{{.Count}} folder(s)"). A "Count" key also
// selects the message's plural form (go-i18n's CLDR plural rules), so
// count-dependent messages (see locales/active.*.toml's "= n" blocks)
// don't need a separate message ID per language's plural forms.
type TplData map[string]any

// T looks up messageID in the resolved language, interpolating data[0] if
// given. A missing message ID falls back to messageID itself rather than
// an empty string or panic — a visible, greppable "obviously untranslated"
// marker in the rendered page beats either silently blank UI or a 500.
func (l *Localizer) T(messageID string, data ...TplData) string {
	cfg := &goi18n.LocalizeConfig{MessageID: messageID}
	if len(data) > 0 {
		cfg.TemplateData = map[string]any(data[0])
		if count, ok := data[0]["Count"]; ok {
			cfg.PluralCount = count
		}
	}
	msg, err := l.loc.Localize(cfg)
	if err != nil {
		return messageID
	}
	return msg
}
