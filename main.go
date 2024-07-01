package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/digitalocean/godo"
	gha "github.com/sethvargo/go-githubactions"
	"sigs.k8s.io/yaml"
)

func main() {
	ctx := context.Background()
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
		action: a,
		apps:   godo.NewFromToken(in.token).Apps,
		ghCtx:  ghCtx,
		inputs: in,
	}
	app, err := d.deploy(ctx)
	if app != nil {
		// Surface a JSON representation of the app regardless of success or failure.
		appJSON, err := json.Marshal(app)
		if err != nil {
			a.Errorf("failed to marshal app: %v", err)
		}
		a.SetOutput("app", string(appJSON))
	}
	if err != nil {
		a.Fatalf("failed to deploy: %v", err)
	}
	a.Infof("App is now live under URL: %s", app.GetLiveURL())
}

type deployer struct {
	action *gha.Action
	apps   godo.AppsService
	ghCtx  *gha.GitHubContext
	inputs inputs
}

// deploy deploys the app and waits for it to be live.
func (d *deployer) deploy(ctx context.Context) (*godo.App, error) {
	// First, fetch the app spec either from a pre-existing app or from the file system.
	var spec *godo.AppSpec
	if d.inputs.appName != "" {
		app, err := d.getAppWithName(ctx, d.inputs.appName)
		if err != nil {
			return nil, fmt.Errorf("failed to get app: %w", err)
		}
		if app == nil {
			return nil, fmt.Errorf("app %q does not exist", d.inputs.appName)
		}
		spec = app.Spec
	} else {
		appSpec, err := os.ReadFile(d.inputs.appSpecLocation)
		if err != nil {
			return nil, fmt.Errorf("failed to get app spec content: %w", err)
		}
		if err := yaml.Unmarshal(appSpec, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse app spec: %w", err)
		}
	}

	if d.inputs.deployPRPreview {
		// If this is a PR preview, we need to sanitize the spec.
		if err := sanitizeSpecForPullRequestPreview(spec, d.ghCtx); err != nil {
			return nil, fmt.Errorf("failed to sanitize spec for PR preview: %w", err)
		}
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

	ds, _, err := d.apps.ListDeployments(ctx, app.GetID(), &godo.ListOptions{PerPage: 1})
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

		if d.inputs.printBuildLogs {
			d.action.Group("build logs")
			d.action.Infof(string(buildLogs))
			d.action.EndGroup()
		}
	}

	deployLogs, err := d.getLogs(ctx, app.ID, deploymentID, godo.AppLogTypeDeploy)
	if err != nil {
		return nil, fmt.Errorf("failed to get deploy logs: %w", err)
	}
	if len(deployLogs) > 0 {
		d.action.SetOutput("deploy_logs", string(deployLogs))

		if d.inputs.printDeployLogs {
			d.action.Group("deploy logs")
			d.action.Infof(string(deployLogs))
			d.action.EndGroup()
		}
	}

	if dep.Phase != godo.DeploymentPhase_Active {
		// Fetch the app to get the latest state before returning.
		app, _, err := d.apps.Get(ctx, app.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get app after it failed: %w", err)
		}
		return app, fmt.Errorf("deployment failed: %s", dep.Phase)
	}

	app, err = d.waitForAppLiveURL(ctx, app.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for app to have a live URL: %w", err)
	}

	return app, nil
}

// getAppWithName returns the app with the given name, or nil if it does not exist.
func (d *deployer) getAppWithName(ctx context.Context, name string) (*godo.App, error) {
	// TODO: Implement pagination.
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

// isInTerminalPhase returns whether or not the given deployment is in a terminal phase.
func isInTerminalPhase(d *godo.Deployment) bool {
	switch d.GetPhase() {
	case godo.DeploymentPhase_Active, godo.DeploymentPhase_Error, godo.DeploymentPhase_Canceled, godo.DeploymentPhase_Superseded:
		return true
	}
	return false
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

// getLogs retrieves the logs from the given historic URLs.
func (d *deployer) getLogs(ctx context.Context, appID, deploymentID string, typ godo.AppLogType) ([]byte, error) {
	logsResp, resp, err := d.apps.GetLogs(ctx, appID, deploymentID, "", typ, true, -1)
	if err != nil {
		// Ignore if we get a 400, as this means the respective state was never reached or skipped.
		if resp.StatusCode == http.StatusBadRequest {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to get deploy logs: %w", err)
	}

	var buf bytes.Buffer
	for _, historicURL := range logsResp.HistoricURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, historicURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create log request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to get historic logs: %w", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read historic logs: %w", err)
		}
		buf.Write(body)
	}
	return buf.Bytes(), nil
}
