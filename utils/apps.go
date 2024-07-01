package utils

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

// FindAppByName returns the app with the given name, or nil if it does not exist.
func FindAppByName(ctx context.Context, ap godo.AppsService, name string) (*godo.App, error) {
	// TODO: Implement pagination.
	apps, _, err := ap.List(ctx, &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}

	for _, a := range apps {
		if a.GetSpec().GetName() == name {
			return a, nil
		}
	}
	return nil, nil
}
