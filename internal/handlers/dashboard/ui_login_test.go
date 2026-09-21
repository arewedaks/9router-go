package dashboard

import (
	"strings"
	"testing"
)

// Sign out sits in the top-right corner of the header, not at the foot of the
// sidebar rail. The rail has its own scroll, so a control at its bottom could sit
// below the fold on a short window — an app-level action should not be reachable
// only after scrolling.
func TestSignOutIsInTheHeaderTopRight(t *testing.T) {
	ui := readEmbeddedUI(t)

	headerAt := strings.Index(ui, "<header>")
	if headerAt < 0 {
		t.Fatal("no <header> in the served UI")
	}
	headerEnd := strings.Index(ui[headerAt:], "</header>")
	if headerEnd < 0 {
		t.Fatal("unclosed <header>")
	}
	header := ui[headerAt : headerAt+headerEnd]

	if !strings.Contains(header, `id="auth-status-btn"`) {
		t.Error("the sign-out button is not inside the header")
	}
	if !strings.Contains(header, "signOut()") {
		t.Error("the header sign-out button is not wired to signOut()")
	}

	// It must follow the Go Engine badge in source order, which is what puts it
	// on the right-hand side of the flex row.
	badge := strings.Index(header, "badge-go")
	btn := strings.Index(header, `id="auth-status-btn"`)
	if badge < 0 || btn < 0 || btn < badge {
		t.Error("sign-out does not sit after the header badge, so it will not be " +
			"at the right-hand end of the header")
	}

	// The old rail footer must be gone, or the button would render in both
	// places.
	if strings.Contains(ui, "sidebar-foot") {
		t.Error("the sidebar footer still exists; sign-out would appear twice")
	}
}

// The dashboard must be reachable only after the password login. It used to be
// a modal over a mounted shell, so the sidebar, title and empty panes were on
// screen behind it — and the tab title already carried the version, which made
// it read as a signed-in app.
func TestLoginIsAFullPageNotAModal(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `id="login-screen"`) {
		t.Fatal("no login screen element")
	}
	// A modal-overlay class would put it back on top of the dashboard.
	if at := strings.Index(ui, `id="login-screen"`); at >= 0 {
		start := at - 120
		if start < 0 {
			start = 0
		}
		if strings.Contains(ui[start:at], "modal-overlay") {
			t.Error("the login screen is still a modal-overlay, so the dashboard " +
				"remains visible behind it before sign-in")
		}
	}

	// The shell must be taken out of the flow while signed out, or a keyboard
	// user could tab into controls they cannot see.
	if !strings.Contains(ui, "body.needs-login .shell") {
		t.Error("no rule hides the shell while signed out; the dashboard would " +
			"stay visible and focusable behind the login screen")
	}
	if !strings.Contains(ui, `classList.add("needs-login")`) {
		t.Error("nothing sets needs-login")
	}
}

// The class has to be applied before the first paint. The main bundle runs only
// after checkAuth() resolves, so doing it there leaves the dashboard visible for
// the duration of the status request.
func TestShellIsHiddenBeforeFirstPaint(t *testing.T) {
	ui := readEmbeddedUI(t)

	body := strings.Index(ui, "<body>")
	if body < 0 {
		t.Fatal("no <body> in the served UI")
	}
	// Look only at the markup before the sidebar starts: a synchronous inline
	// script has to sit there.
	sidebar := strings.Index(ui[body:], `id="sidebar"`)
	if sidebar < 0 {
		t.Fatal("no sidebar in the served UI")
	}
	head := ui[body : body+sidebar]
	if !strings.Contains(head, "classList.add(\"needs-login\")") {
		t.Error("needs-login is not set before the sidebar markup, so the " +
			"dashboard paints before checkAuth can hide it")
	}
}

// Login is password-only. A client API key authorises the model proxy; it must
// not open the dashboard, and upstream VansRouter has never allowed it either.
func TestLoginHasNoAPIKeyPath(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, gone := range []string{
		"Use an API key instead",
		`id="login-key-input"`,
		`id="login-key-fields"`,
		"function submitKeyLogin(",
		"function setLoginMode(",
		"function toggleLoginMode(",
		// The stored-key session cache goes with the path that created it.
		`localStorage.getItem("nine_api_key")`,
	} {
		if strings.Contains(ui, gone) {
			t.Errorf("the login screen still offers the API-key path: %q", gone)
		}
	}

	// The password path must still be wired end to end.
	for _, need := range []string{
		`id="login-password-input"`,
		"function submitPasswordLogin(",
		`/api/dashboard/auth/login`,
		"function showLoginScreen(",
		"function hideLoginScreen(",
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("missing %q: the password login is not wired up", need)
		}
	}
}

// A stale key in localStorage from an older build must be cleared rather than
// left to be picked up by anything later.
func TestStaleAPIKeyIsCleared(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `localStorage.removeItem("nine_api_key")`) {
		t.Error("a stored nine_api_key from a build that had the API-key login " +
			"is never cleared")
	}
}
