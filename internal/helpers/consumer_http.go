package helpers

import (
	"context"
	"errors"
	"fmt"
)

type consumerGetter interface {
	ConsumerGET(ctx context.Context, consumerService, targetService string, targetPort int, route, apiKey string) (status int, body []byte, err error)
}

// ConsumerGET reads one literal route of the installed target from inside this
// App's own container, using the target's internal Address and a private
// header credential. Errors never carry the key, response or command output.
func (h *H) ConsumerGET(ctx context.Context, target *H, route, apiKey string) (Response, error) {
	if target == nil {
		return Response{}, fmt.Errorf("%w: no installed target", errors.ErrUnsupported)
	}
	executor, ok := h.Config.Exec.(consumerGetter)
	if !ok {
		return Response{}, fmt.Errorf("%w: this executor cannot read from the consumer's network", errors.ErrUnsupported)
	}
	h.Log.Secret(apiKey)
	status, body, err := executor.ConsumerGET(ctx, h.Service, target.Service, target.ContainerPort, route, apiKey)
	if err != nil {
		return Response{}, h.Log.RedactError(err)
	}
	return Response{Status: status, Body: body}, nil
}
