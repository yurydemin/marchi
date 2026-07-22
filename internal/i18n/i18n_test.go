package i18n

import "testing"

func TestNewLocalizer_ResolvesSupportedAndFallsBackToDefault(t *testing.T) {
	if got := NewLocalizer("ru").Lang; got != "ru" {
		t.Fatalf("Lang = %q, want ru", got)
	}
	if got := NewLocalizer("fr").Lang; got != Default {
		t.Fatalf("Lang = %q, want default %q", got, Default)
	}
	if got := NewLocalizer("").Lang; got != Default {
		t.Fatalf("Lang = %q, want default %q", got, Default)
	}
}

func TestLocalizer_T_SimpleMessage(t *testing.T) {
	en := NewLocalizer("en")
	if got := en.T("nav.dashboard"); got != "Dashboard" {
		t.Fatalf("T(nav.dashboard) = %q, want %q", got, "Dashboard")
	}
	ru := NewLocalizer("ru")
	if got := ru.T("nav.dashboard"); got == "Dashboard" || got == "nav.dashboard" {
		t.Fatalf("T(nav.dashboard) in ru = %q, want a Russian translation", got)
	}
}

func TestLocalizer_T_MissingMessage_FallsBackToID(t *testing.T) {
	en := NewLocalizer("en")
	if got := en.T("does.not.exist"); got != "does.not.exist" {
		t.Fatalf("T(does.not.exist) = %q, want the message ID back", got)
	}
}

func TestLocalizer_T_Interpolated(t *testing.T) {
	en := NewLocalizer("en")
	got := en.T("dashboard.s3_queue_detail", TplData{"Uploading": 2, "Failed": 1})
	want := "2 uploading, 1 failed"
	if got != want {
		t.Fatalf("T(dashboard.s3_queue_detail) = %q, want %q", got, want)
	}
}

func TestLocalizer_T_Plural(t *testing.T) {
	en := NewLocalizer("en")
	one := en.T("accounts.test_result_ok", TplData{"Count": 1})
	other := en.T("accounts.test_result_ok", TplData{"Count": 3})
	if one == other {
		t.Fatalf("plural forms should differ: one=%q other=%q", one, other)
	}
}
