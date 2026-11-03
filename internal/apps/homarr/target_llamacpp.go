package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

const llamacppTransportHold = "llama.cpp native connection is held before integration creation: the audited Homarr llamacpp adapter sends no authentication and its data reads call /slots and router /metrics, which can wake a sleeping model or autoload models. Preserve this connection. Retry self-host setup homarr only after an audited adapter reads without waking or loading models. Do not enable authentication bypass, disable sleep or autoload, load models, or change the llama.cpp server to satisfy this hold; connect it manually in Homarr if you accept those effects."

func llamacppExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "llamacpp" {
		return target{}, errors.New("the llama.cpp external adapter requires the exact llamacpp kind")
	}
	if err := externalHTTPOrigin(c.Endpoint, "llama.cpp"); err != nil {
		return target{}, err
	}
	if len(c.Credentials) != 0 {
		return target{}, errors.New("llama.cpp supports only the unauthenticated alternative; Homarr sends no authentication, so an authenticated server must be connected manually")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("llama.cpp takes no selection; supply only the endpoint")
	}
	return target{
		name: "llama.cpp", kind: "llamacpp",
		widgets: []widget{statWidget("health")},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: llamacppTransportHold}
		},
		verify: func(context.Context, *helpers.H, *helpers.H, string) error {
			return &helpers.ManualError{Hint: llamacppTransportHold}
		},
		stats: func(statsSample) error {
			return &helpers.ManualError{Hint: llamacppTransportHold}
		},
	}, nil
}
