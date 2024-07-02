package utils

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

func TestFindAppByName(t *testing.T) {
	app1 := &godo.App{Spec: &godo.AppSpec{Name: "app1"}}
	app2 := &godo.App{Spec: &godo.AppSpec{Name: "app2"}}

	tests := []struct {
		name     string
		apps     []*godo.App
		appName  string
		expected *godo.App
	}{{
		name:     "app1",
		apps:     []*godo.App{app1, app2},
		appName:  "app1",
		expected: app1,
	}, {
		name:     "app2",
		apps:     []*godo.App{app1, app2},
		appName:  "app2",
		expected: app2,
	}, {
		name:     "not found",
		apps:     []*godo.App{app1, app2},
		appName:  "app3",
		expected: nil,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			do := &fakeAppsService{apps: test.apps}
			app, err := FindAppByName(context.Background(), do, test.appName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if app != test.expected {
				t.Errorf("expected app %v, got %v", test.expected, app)
			}
		})
	}
}

type fakeAppsService struct {
	godo.AppsService
	apps      []*godo.App
	listError error
}

func (f *fakeAppsService) List(ctx context.Context, opt *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return f.apps, nil, f.listError
}
