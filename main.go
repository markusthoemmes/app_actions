package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/app_action/proxyclient"
	"github.com/digitalocean/godo"
	gha "github.com/sethvargo/go-githubactions"
	"sigs.k8s.io/yaml"
)

const (
	inputDeployPreview = "deploy_preview"
	inputToken         = "token"
)

func main() {
	doToken := gha.GetInput(inputToken)
	if doToken == "" {
		gha.Fatalf("missing input %q", inputToken)
	}
	gha.AddMask(doToken)

	deployPreviewStr := gha.GetInput(inputDeployPreview)
	var deployPreview bool
	var err error
	if deployPreviewStr != "" {
		deployPreview, err = strconv.ParseBool(deployPreviewStr)
		if err != nil {
			gha.Fatalf("failed to parse input %q: %v", inputDeployPreview, err)
		}
	}

	ghCtx, err := gha.Context()
	if err != nil {
		gha.Fatalf("failed to get context: %v", err)
	}

	do := godo.NewFromToken(doToken)

	d := &deployer{
		apps:    do.Apps,
		ghCtx:   ghCtx,
		preview: deployPreview,
	}
	app, err := d.deploy(context.Background())
	if err != nil {
		gha.Fatalf("failed to deploy: %v", err)
	}
	gha.Infof("live URL: %s", app.GetLiveURL())

	appJSON, err := json.Marshal(app)
	if err != nil {
		gha.Fatalf("failed to marshal app: %v", err)
	}
	gha.SetOutput("app", string(appJSON))
}

type deployer struct {
	apps    godo.AppsService
	ghCtx   *gha.GitHubContext
	preview bool
}

func (d *deployer) deploy(ctx context.Context) (*godo.App, error) {
	appSpec, err := os.ReadFile(".do/app.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to get app spec content: %w", err)
	}
	var spec godo.AppSpec
	if err := yaml.Unmarshal([]byte(appSpec), &spec); err != nil {
		return nil, fmt.Errorf("failed to parse app spec: %w", err)
	}

	repoOwner, repo := d.ghCtx.Repo()

	if d.preview {
		// Override app name to something that identifies this PR.
		spec.Name = generateAppName(repoOwner, repo, d.ghCtx.RefName)
		gha.Infof("app name: %s", spec.Name)

		// Unset any domains as those might collide with production apps.
		spec.Domains = nil

		// Unset any alerts as those will be delivered wrongly anyway.
		spec.Alerts = nil

		// Override the reference of all relevant components to point to the PRs ref.
		var githubRefs []*godo.GitHubSourceSpec
		for _, svc := range spec.GetServices() {
			if svc.GetGitHub() != nil {
				githubRefs = append(githubRefs, svc.GetGitHub())
			}
		}
		for _, worker := range spec.GetWorkers() {
			if worker.GetGitHub() != nil {
				githubRefs = append(githubRefs, worker.GetGitHub())
			}
		}
		for _, job := range spec.GetJobs() {
			if job.GetGitHub() != nil {
				githubRefs = append(githubRefs, job.GetGitHub())
			}
		}
		for _, ref := range githubRefs {
			if ref.Repo != fmt.Sprintf("%s/%s", repoOwner, repo) {
				// Skip Github refs pointing to other repos.
				continue
			}
			// We manually kick new deployments so we can watch their status better.
			ref.DeployOnPush = false
			ref.Branch = d.ghCtx.RefName
		}
	}

	gha.Infof("start deployment of app %q", spec.Name)
	apps, _, err := d.apps.List(ctx, &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}

	var app *godo.App
	for _, a := range apps {
		if a.GetSpec().GetName() == spec.GetName() {
			app = a
			break
		}
	}
	if app == nil {
		// App does not exist yet, create it.
		app, _, err = d.apps.Create(ctx, &godo.AppCreateRequest{Spec: &spec})
		if err != nil {
			return nil, fmt.Errorf("failed to create app: %w", err)
		}
	} else {
		// App already exists, update it.
		app, _, err = d.apps.Update(ctx, app.GetID(), &godo.AppUpdateRequest{Spec: &spec})
		if err != nil {
			return nil, fmt.Errorf("failed to update app: %w", err)
		}
	}

	ds, _, err := d.apps.ListDeployments(ctx, app.GetID(), &godo.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}
	deploymentID := ds[0].GetID()

	gha.Infof("wait for deployment to finish")
	dep, err := d.waitForDeploymentTerminal(ctx, app.ID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to wait deployment to finish: %w", err)
	}

	buildLogsResp, resp, err := d.apps.GetLogs(ctx, app.ID, deploymentID, "", godo.AppLogTypeBuild, true, -1)
	if err != nil {
		// Ignore if we get a 400, as this means the build logs got or the state was never reached.
		if resp.StatusCode != http.StatusBadRequest {
			return nil, fmt.Errorf("failed to get build logs: %w", err)
		}
	} else {
		gha.Group("build logs")
		buildLogs, err := getLogs(ctx, buildLogsResp.HistoricURLs)
		if err != nil {
			return nil, fmt.Errorf("failed to get build logs: %w", err)
		}
		printLogs(buildLogs)
		gha.SetOutput("build_logs", string(buildLogs))
		gha.EndGroup()
	}

	deployLogsResp, _, err := d.apps.GetLogs(ctx, app.ID, deploymentID, "", godo.AppLogTypeDeploy, true, -1)
	if err != nil {
		// Ignore if we get a 400, as this means the deploy state was never reached.
		if resp.StatusCode != http.StatusBadRequest {
			return nil, fmt.Errorf("failed to get deploy logs: %w", err)
		}
	} else {
		gha.Group("deploy logs")
		deployLogs, err := getLogs(ctx, deployLogsResp.HistoricURLs)
		if err != nil {
			return nil, fmt.Errorf("failed to get deploy logs: %w", err)
		}
		printLogs(deployLogs)
		gha.SetOutput("deploy_logs", string(deployLogs))
		gha.EndGroup()
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

// waitForDeploymentTerminal waits for the given deployment to be in a terminal state.
func (d *deployer) waitForDeploymentTerminal(ctx context.Context, appID, deploymentID string) (*godo.Deployment, error) {
	t := time.NewTicker(2 * time.Second)

	var dep *godo.Deployment
	var currentPhase godo.DeploymentPhase
	for !isInTerminalPhase(dep) {
		var err error
		dep, _, err = d.apps.GetDeployment(ctx, appID, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment: %w", err)
		}

		if currentPhase != dep.GetPhase() {
			gha.Infof("deployment is in phase: %s", dep.GetPhase())
			currentPhase = dep.GetPhase()

			// TODO: Consider streaming logs as implemented below. There seems to be an issue with
			// the websocket not closing even if a log is already at the end.
			/*if currentPhase == godo.DeploymentPhase_Building {
				followResp, _, err := d.apps.GetLogs(ctx, appID, deploymentID, "", godo.AppLogTypeBuild, true, -1)
				if err != nil {
					return nil, fmt.Errorf("failed to get build logs: %w", err)
				}

				streamLogs(ctx, followResp.LiveURL)
			} else if currentPhase == godo.DeploymentPhase_Deploying {
				time.Sleep(3 * time.Second)

				followResp, _, err := d.apps.GetLogs(ctx, appID, deploymentID, "", godo.AppLogTypeDeploy, true, -1)
				if err != nil {
					return nil, fmt.Errorf("failed to get deployment logs: %w", err)
				}

				streamLogs(ctx, followResp.LiveURL)
			}*/
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

func getLogs(ctx context.Context, historicURLs []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, historicURL := range historicURLs {
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

func printLogs(logs []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(logs))
	for scanner.Scan() {
		gha.Infof(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		gha.Fatalf("failed to read logs: %v", err)
	}
}

func streamLogs(ctx context.Context, liveURL string) error {
	followLiveURL, err := url.Parse(liveURL)
	if err != nil {
		return fmt.Errorf("failed to parse live URL: %w", err)
	}
	followLiveURL.Scheme = "wss" // Use a websocket connection to avoid buffers to interfere.

	followLogs, err := proxyclient.Logs(ctx, followLiveURL.String())
	if err != nil {
		return fmt.Errorf("failed to stream logs: %w", err)
	}
	defer followLogs.Close()

	scanner := bufio.NewScanner(followLogs)
	for scanner.Scan() {
		gha.Infof(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read logs: %w", err)
	}

	return nil
}

func generateAppName(repoOwner, repo, ref string) string {
	baseName := fmt.Sprintf("%s-%s", repoOwner, repo)
	baseName = strings.ToLower(baseName)
	baseName = strings.NewReplacer(
		"/", "-", // Replace slashes.
		":", "", // Colons are illegal.
		"_", "-", // Underscores are illegal.
	).Replace(baseName)

	// Generate a hash from the completely unique enumeration of repoOwner, repo, and ref.
	unique := fmt.Sprintf("%s-%s-%s", repoOwner, repo, ref)
	hasher := sha256.New()
	hasher.Write([]byte(unique))
	suffix := "-" + hex.EncodeToString(hasher.Sum(nil))[:8]

	// App names must be at most 32 characters.
	limit := 32 - len(suffix)
	if len(baseName) < limit {
		limit = len(baseName)
	}

	return baseName[:limit] + suffix
}
