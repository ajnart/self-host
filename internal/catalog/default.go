package catalog

// Default is the catalog shipped in the binary.
func Default() *Catalog {
	return New(append(registeredApps(), specApps()...), defaultRecipes)
}
