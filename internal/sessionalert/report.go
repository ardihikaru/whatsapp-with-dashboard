package sessionalert

import (
	"fmt"
	"strings"
)

// buildLogoutAlertText renders one batched message naming every
// logged_out session, plus an optional "recovered" line for any session
// that came back up before this batch flushed. Pure so it is directly
// unit-testable.
func buildLogoutAlertText(batch []pendingEntry) string {
	var loggedOut, recovered []pendingEntry
	for _, e := range batch {
		switch e.Kind {
		case kindLoggedOut:
			loggedOut = append(loggedOut, e)
		case kindRecovered:
			recovered = append(recovered, e)
		}
	}
	var b strings.Builder
	if len(loggedOut) == 1 {
		fmt.Fprintf(&b, "⚠️ Session logged out: %s (%s)", loggedOut[0].Name, loggedOut[0].Phone)
	} else if len(loggedOut) > 1 {
		fmt.Fprintf(&b, "⚠️ %d sessions logged out:", len(loggedOut))
		for _, e := range loggedOut {
			fmt.Fprintf(&b, "\n- %s (%s)", e.Name, e.Phone)
		}
	}
	if len(recovered) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Recovered since:")
		for _, e := range recovered {
			fmt.Fprintf(&b, "\n- %s (%s)", e.Name, e.Phone)
		}
	}
	return b.String()
}

// buildDailyReportText renders the once-a-day roundup of every
// non-connected session, marking a deliberately deactivated one
// distinctly from an unexplained outage. total is every session's own
// count (connected + not), so "All N sessions connected" can be reported
// correctly when notConnected is empty.
func buildDailyReportText(notConnected []Snapshot, total int) string {
	if len(notConnected) == 0 {
		return fmt.Sprintf("All %d sessions connected ✅", total)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Daily session report: %d of %d session(s) not connected:", len(notConnected), total)
	for _, sn := range notConnected {
		status := sn.State
		if !sn.Active {
			status = sn.State + " (deactivated)"
		} else if sn.StalledReason != "" {
			status = sn.State + " — " + sn.StalledReason
		}
		fmt.Fprintf(&b, "\n- %s (%s): %s", sn.Name, sn.Phone, status)
	}
	return b.String()
}
