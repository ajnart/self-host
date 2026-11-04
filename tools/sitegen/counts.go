package main

import "github.com/ajnart/self-host/internal/catalog"

type countsJSON struct {
	Apps       int `json:"apps"`
	Recipes    int `json:"recipes"`
	AllApps    int `json:"allApps"`
	AllRecipes int `json:"allRecipes"`
}

// catalogCounts is the one source of every App and Recipe count. The visible
// numbers are what a person is offered; the "all" numbers add what the Catalog
// still carries for existing installs and e2e targets, among them the hidden
// homarr-control-plane compatibility Recipe.
func catalogCounts(cat *catalog.Catalog) countsJSON {
	allRecipes := len(cat.Recipes())
	if _, ok := cat.Recipe("homarr-control-plane"); ok {
		allRecipes++
	}
	return countsJSON{
		Apps:       len(cat.Apps()),
		Recipes:    len(cat.Recipes()),
		AllApps:    len(cat.AllApps()),
		AllRecipes: allRecipes,
	}
}
