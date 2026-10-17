package registry

import "testing"

// TestContactsToolsAreNotSessionScoped is the RED-without-the-fix test
// named in the task: routeSpec.tool() treats any route containing the
// literal substring "{id}" as session-scoped and adds session_id/phone
// params to resolve it — routes_api.go's Contacts routes use "{contactID}"
// specifically so that does NOT fire (a contact id is not a session id).
// This pins both halves: the tool's params must carry "contactID" (not
// "id"), and must NOT carry "session_id"/"phone" at all.
func TestContactsToolsAreNotSessionScoped(t *testing.T) {
	tools := map[string]bool{}
	for _, tool := range wsmRouteTools() {
		switch tool.Name {
		case "wsm_contacts_list", "wsm_contacts_create", "wsm_contacts_delete", "wsm_contacts_verify":
			tools[tool.Name] = true
			var names []string
			for _, p := range tool.Params {
				names = append(names, p.Name)
			}
			for _, bad := range []string{"session_id", "phone"} {
				for _, n := range names {
					if n == bad {
						t.Errorf("tool %q carries param %q — contacts routes are GLOBAL, not session-scoped; params: %v", tool.Name, bad, names)
					}
				}
			}
		}
	}
	for _, want := range []string{"wsm_contacts_list", "wsm_contacts_create", "wsm_contacts_delete", "wsm_contacts_verify"} {
		if !tools[want] {
			t.Errorf("wsmRouteTools() has no tool named %q", want)
		}
	}
}

// TestContactsDeleteAndVerifyRequireContactID pins that the path parameter
// is named "contactID" (required), the name routes_api.go actually uses,
// not the generic "id" routeSpec reserves for session resolution.
func TestContactsDeleteAndVerifyRequireContactID(t *testing.T) {
	for _, tool := range wsmRouteTools() {
		if tool.Name != "wsm_contacts_delete" && tool.Name != "wsm_contacts_verify" {
			continue
		}
		found := false
		for _, p := range tool.Params {
			if p.Name == "contactID" {
				found = true
				if !p.Required {
					t.Errorf("tool %q: contactID param is not required", tool.Name)
				}
			}
		}
		if !found {
			t.Errorf("tool %q has no contactID param", tool.Name)
		}
	}
}
