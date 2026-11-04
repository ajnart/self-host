package helpers_test

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type consumerExec struct {
	fakeExec
	got    []string
	status int
	err    error
}

func (e *consumerExec) ConsumerGET(_ context.Context, consumer, target string, port int, route, _ string) (int, []byte, error) {
	e.got = []string{consumer, target, route}
	return e.status, []byte("body"), e.err
}

func TestConsumerGETReportsUnsupportedExecutors(t *testing.T) {
	from := newH(t, nil, &fakeExec{}, nil)
	to := newH(t, nil, &fakeExec{}, nil)
	if _, err := from.ConsumerGET(context.Background(), to, "/api", "key"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("an executor without the capability must be unsupported")
	}
	if _, err := from.ConsumerGET(context.Background(), nil, "/api", "key"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("a missing target must be unsupported")
	}
}

func TestConsumerGETUsesTheConsumerAndTargetServices(t *testing.T) {
	exec := &consumerExec{status: 200}
	from := newH(t, nil, exec, nil)
	to := newH(t, nil, &fakeExec{}, nil)
	response, err := from.ConsumerGET(context.Background(), to, "/api", "key")
	if err != nil || response.Status != 200 || string(response.Body) != "body" || len(exec.got) != 3 || exec.got[0] != from.Service || exec.got[1] != to.Service {
		t.Fatalf("response=%v err=%v got=%v", response, err, exec.got)
	}
}

func TestConsumerGETErrorsDoNotCarryTheKey(t *testing.T) {
	exec := &consumerExec{err: errors.New("failed with the-secret-key")}
	from := newH(t, nil, exec, nil)
	to := newH(t, nil, &fakeExec{}, nil)
	_, err := from.ConsumerGET(context.Background(), to, "/api", "the-secret-key")
	if err == nil || strings.Contains(err.Error(), "the-secret-key") {
		t.Fatalf("key leaked: %v", err)
	}
}
