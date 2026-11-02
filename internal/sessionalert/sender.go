package sessionalert

// chooseSender picks which session sends an alert, in order: the
// configured id (if connected and not excluded), else whichever session
// carries SelfReminder (if connected and not excluded), else any
// connected, non-excluded session. excluded is the set of session ids
// being reported on (the logout batch, or the daily report's
// non-connected list) — a sender must never be one of them, though in
// practice a reported session is never Connected anyway and so would be
// filtered out regardless; excluded makes that guarantee explicit rather
// than incidental.
//
// Pure and dependency-free so it is directly unit-testable without a
// Gateway or a clock, mirroring wa.SelectSnapshotForRoute's own split
// between the live Manager method and its pure ranking helper.
func chooseSender(snaps []Snapshot, configuredID string, excluded map[string]bool) (Snapshot, bool) {
	if configuredID != "" {
		for _, sn := range snaps {
			if sn.ID == configuredID && sn.Connected && !excluded[sn.ID] {
				return sn, true
			}
		}
	}
	for _, sn := range snaps {
		if sn.SelfReminder && sn.Connected && !excluded[sn.ID] {
			return sn, true
		}
	}
	for _, sn := range snaps {
		if sn.Connected && !excluded[sn.ID] {
			return sn, true
		}
	}
	return Snapshot{}, false
}
