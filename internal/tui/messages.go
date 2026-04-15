// Package tui is a bubbletea v2 + lipgloss v2 client for the teams_con API.
//
// Layout mirrors the waf_con tab pattern: a root Model owns an array of
// tab views and dispatches key/window messages to the active tab. All HTTP
// is performed through a single *Client (client.go) and wrapped in tea.Cmds
// so the model itself stays goroutine-free.
package tui

import "teams_con/internal/store"

// SwitchToCallsMsg is emitted by tab_users when the user presses enter on a
// row. The root Model catches it, flips the active tab to tabCalls, and
// forwards the message to tab_calls so it can re-fetch with the new filter.
type SwitchToCallsMsg struct {
	UPN string
}

// RefreshMsg is dispatched by the root model to the ACTIVE tab only when the
// user presses 'r'. It is intentionally not broadcast to all tabs — each tab
// refreshes on demand, avoiding unnecessary fetches for tabs the user is not
// currently viewing.
type RefreshMsg struct{}

// OpenDrillMsg is emitted by tab_calls when the user presses enter on a
// row. The root Model flips the active tab to tabDrill and forwards the
// message so the drill tab can fetch the call detail.
type OpenDrillMsg struct {
	CallId string
}

// BackToCallsMsg is emitted by tab_drill when the user presses 'b' or
// backspace. The root Model catches it and flips the active tab back to
// tabCalls without touching any other state.
type BackToCallsMsg struct{}

// callsLoadedMsg delivers the result of Client.ListCalls into tab_calls.
// err != nil means the fetch failed — the tab renders the error in place.
type callsLoadedMsg struct {
	calls []store.Call
	err   error
}

// usersLoadedMsg delivers the result of Client.ListUsers into tab_users.
type usersLoadedMsg struct {
	users []store.UserStat
	err   error
}

// healthLoadedMsg delivers the result of Client.Health into the root Model
// for rendering in the status bar. Both fields may be zero on error.
type healthLoadedMsg struct {
	health *HealthDTO
	err    error
}

// OpenUserPortraitMsg is emitted when the user presses P on a users row
// or enter on a flaky-mic row. The root Model flips the active tab to
// tabUsers and forwards the message so tab_users enters portrait mode
// and fetches the report for UPN.
type OpenUserPortraitMsg struct {
	UPN string
}

// userHealthFetchedMsg delivers the result of Client.GetUserHealth into
// the users tab's portrait sub-view. seq is the monotonic stale-guard
// key — each openPortrait bumps the tab's counter and tags the outgoing
// request with it; any reply whose seq does not match the tab's current
// request counter is dropped. A plain UPN match is insufficient because
// the user can open→close→reopen the same portrait faster than the
// network replies, yielding duplicate "fresh" responses.
type userHealthFetchedMsg struct {
	seq    int
	upn    string
	report *UserHealthReportDTO
	err    error
}

// flakyMicsLoadedMsg delivers the result of Client.ListFlakyMics into the
// flaky mics tab.
type flakyMicsLoadedMsg struct {
	mics []FlakyMicDTO
	err  error
}
