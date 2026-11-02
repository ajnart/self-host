//go:build !linux

package system

import (
	"context"
	"fmt"
	"os"
)

func inspectDockerNamespace(context.Context, string) (*UserNamespace, error) {
	return nil, fmt.Errorf("local Docker user namespaces require Linux")
}

func setNamespaceMetadata(context.Context, *UserNamespace, string, int, int, os.FileMode) error {
	return fmt.Errorf("local Docker user namespaces require Linux")
}
