package daemon

import "github.com/google/uuid"

// migrateToDefaultProject upgrades a pre-projects workspace state in place.
// State written before this feature has no "projects" key; every tab in it
// belonged to one implicit workspace, so it becomes one project named
// "Default" with tab order preserved exactly.
//
// It operates on map[string]any because internal/persist is deliberately
// schema-free — the workspace schema lives here. RootDir is left empty; the
// caller fills it from its own os.Getwd(), which this function must not guess.
func migrateToDefaultProject(state map[string]any) {
	if existing, ok := state["projects"].([]any); ok && len(existing) > 0 {
		return
	}
	tabs, ok := state["tabs"].([]any)
	if !ok || len(tabs) == 0 {
		return
	}

	id := "proj-" + uuid.New().String()[:8]
	tabIDs := make([]any, 0, len(tabs))
	for _, raw := range tabs {
		tab, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tab["project_id"] = id
		if tabID, ok := tab["id"].(string); ok {
			tabIDs = append(tabIDs, tabID)
		}
	}

	activeTab, _ := state["active_tab"].(string)
	if activeTab == "" && len(tabIDs) > 0 {
		activeTab, _ = tabIDs[0].(string)
	}

	state["projects"] = []any{map[string]any{
		"id": id, "name": "Default", "root_dir": "",
		"tab_ids": tabIDs, "active_tab": activeTab,
	}}
	state["active_project"] = id
}
