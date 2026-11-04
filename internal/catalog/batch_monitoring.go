package catalog

import (
	"github.com/ajnart/self-host/internal/apps/dashdot"
	"github.com/ajnart/self-host/internal/apps/glances"
	"github.com/ajnart/self-host/internal/apps/netdata"
	"github.com/ajnart/self-host/internal/apps/prometheus"
)

func init() {
	register(
		dashdot.App{},
		glances.App{},
		netdata.App{},
		prometheus.App{},
	)
}
