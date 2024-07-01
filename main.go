package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/digitalocean/godo"
	gha "github.com/sethvargo/go-githubactions"
	"sigs.k8s.io/yaml"
)

func main() {
	a := gha.New()

	in, err := getInputs(a)
	if err != nil {
		a.Fatalf("failed to get inputs: %v", err)
	}
	a.AddMask(in.token)

	ghCtx, err := a.Context()
	if err != nil {
		a.Fatalf("failed to get GitHub context: %v", err)
	}

	d := &deployer{
		action:          a,
		apps:            godo.NewFromToken(in.token).Apps,
		ghCtx:           ghCtx,
		printBuildLogs:  in.printBuildLogs,
		printDeployLogs: in.printDeployLogs,
		prPreview:       in.deployPRPreview,
	}
	app, err := d.deploy(context.Background())
	if err != nil {
		a.Fatalf("failed to deploy: %v", err)
	}
	a.Infof("App is now live under URL: %s", app.GetLiveURL())

	appJSON, err := json.Marshal(app)
	if err != nil {
		a.Fatalf("failed to marshal app: %v", err)
	}
	a.SetOutput("app", string(appJSON))
}

type deployer struct {
	action          *gha.Action
	apps            godo.AppsService
	ghCtx           *gha.GitHubContext
	specFromApp     string
	printBuildLogs  bool
	printDeployLogs bool
	prPreview       bool
}

// deploy deploys the app and waits for it to be live.
func (d *deployer) deploy(ctx context.Context) (*godo.App, error) {
	// First, fetch the app spec either from a pre-existing app or from the file system.
	var spec *godo.AppSpec
	if d.specFromApp != "" {
		app, err := d.getAppWithName(ctx, d.specFromApp)
		if err != nil {
			return nil, fmt.Errorf("failed to get app: %w", err)
		}
		if app == nil {
			return nil, fmt.Errorf("app %q does not exist", d.specFromApp)
		}
		spec = app.Spec
	} else {
		appSpec, err := os.ReadFile(".do/app.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to get app spec content: %w", err)
		}
		if err := yaml.Unmarshal(appSpec, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse app spec: %w", err)
		}
	}

	if d.prPreview {
		// If this is a PR preview, we need to sanitize the spec.
		sanitizeSpecForPullRequestPreview(spec, d.ghCtx)
	}

	// Either create or update the app.
	app, err := d.getAppWithName(ctx, spec.GetName())
	if err != nil {
		return nil, fmt.Errorf("failed to get app: %w", err)
	}
	if app == nil {
		d.action.Infof("app %q did not exist yet, creating...", spec.Name)
		app, _, err = d.apps.Create(ctx, &godo.AppCreateRequest{Spec: spec})
		if err != nil {
			return nil, fmt.Errorf("failed to create app: %w", err)
		}
	} else {
		d.action.Infof("app %q already exists, updating...", spec.Name)
		app, _, err = d.apps.Update(ctx, app.GetID(), &godo.AppUpdateRequest{Spec: spec})
		if err != nil {
			return nil, fmt.Errorf("failed to update app: %w", err)
		}
	}

	ds, _, err := d.apps.ListDeployments(ctx, app.GetID(), &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}
	// The latest deployment is the deployment we just created.
	deploymentID := ds[0].GetID()

	d.action.Infof("wait for deployment to finish")
	dep, err := d.waitForDeploymentTerminal(ctx, app.ID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to wait deployment to finish: %w", err)
	}

	buildLogs, err := d.getLogs(ctx, app.ID, deploymentID, godo.AppLogTypeBuild)
	if err != nil {
		return nil, fmt.Errorf("failed to get build logs: %w", err)
	}
	if len(buildLogs) > 0 {
		d.action.SetOutput("build_logs", string(buildLogs))

		if d.printBuildLogs {
			d.action.Group("build logs")
			printLogs(d.action, buildLogs)
			d.action.EndGroup()
		}
	}

	deployLogs, err := d.getLogs(ctx, app.ID, deploymentID, godo.AppLogTypeDeploy)
	if err != nil {
		return nil, fmt.Errorf("failed to get deploy logs: %w", err)
	}
	if len(deployLogs) > 0 {
		d.action.SetOutput("deploy_logs", string(deployLogs))

		if d.printDeployLogs {
			d.action.Group("deploy logs")
			printLogs(d.action, deployLogs)
			d.action.EndGroup()
		}
	}

	if dep.Phase != godo.DeploymentPhase_Active {
		return nil, fmt.Errorf("deployment failed: %s", dep.Phase)
	}

	app, err = d.waitForAppLiveURL(ctx, app.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for app to have a live URL: %w", err)
	}

	return app, nil
}

// getAppWithName returns the app with the given name, or nil if it does not exist.
func (d *deployer) getAppWithName(ctx context.Context, name string) (*godo.App, error) {
	apps, _, err := d.apps.List(ctx, &godo.ListOptions{})
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

// waitForDeploymentTerminal waits for the given deployment to be in a terminal state.
func (d *deployer) waitForDeploymentTerminal(ctx context.Context, appID, deploymentID string) (*godo.Deployment, error) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()

	var dep *godo.Deployment
	var currentPhase godo.DeploymentPhase
	for !isInTerminalPhase(dep) {
		var err error
		dep, _, err = d.apps.GetDeployment(ctx, appID, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment: %w", err)
		}

		if currentPhase != dep.GetPhase() {
			d.action.Infof("deployment is in phase: %s", dep.GetPhase())
			currentPhase = dep.GetPhase()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return dep, nil
}

// waitForAppLiveURL waits for the given app to have a non-empty live URL.
func (d *deployer) waitForAppLiveURL(ctx context.Context, appID string) (*godo.App, error) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()

	var a *godo.App
	for a.GetLiveURL() == "" {
		var err error
		a, _, err = d.apps.Get(ctx, appID)
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return a, nil
}

// isInTerminalPhase returns whether or not the given deployment is in a terminal phase.
func isInTerminalPhase(d *godo.Deployment) bool {
	switch d.GetPhase() {
	case godo.DeploymentPhase_Active, godo.DeploymentPhase_Error, godo.DeploymentPhase_Canceled, godo.DeploymentPhase_Superseded:
		return true
	}
	return false
}
