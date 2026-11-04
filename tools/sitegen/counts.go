package main

import "github.com/ajnart/self-host/internal/catalog"

type countsJSON struct {
	Apps       int `json:"apps"`
	Recipes    int `json:"recipes"`
	AllApps    int `json:"allApps"`
	AllRecipes int `json:"allRecipes"`
}

// catalogCounts is the one source of every App and Recipe count. The visible
// counts are what a newcomer is offered; the all counts include the hidden
// homarr-control-plane compatibility recipe and the hidden Apps.
func catalogCounts(c *catalog.Catalog) countsJSON {
	return countsJSON{
		Apps:       len(c.Apps()),
		Recipes:    len(c.Recipes()),
		AllApps:    len(c.AllApps()),
		AllRecipes: len(c.AllRecipes()),
	}
}
