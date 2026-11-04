package catalog

import (
	"embed"
	"io/fs"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/appspec"
)

//go:embed specs/*.yaml
var specFS embed.FS

// specApps loads the Apps declared as specs/<id>.yaml. A spec that does not
// parse is a build bug, so it panics rather than dropping the App.
func specApps() []app.App {
	sub, err := fs.Sub(specFS, "specs")
	if err != nil {
		panic(err)
	}
	apps, err := appspec.Load(sub)
	if err != nil {
		panic(err)
	}
	return apps
}
