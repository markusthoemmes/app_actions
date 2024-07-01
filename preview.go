package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/digitalocean/godo"
	gha "github.com/sethvargo/go-githubactions"
)

// sanitizeSpecForPullRequestPreview modifies the given AppSpec to be suitable for a pull request preview.
// This includes:
// - Setting a unique app name.
// - Unsetting any domains.
// - Unsetting any alerts.
// - Setting the reference of all relevant components to point to the PRs ref.
func sanitizeSpecForPullRequestPreview(spec *godo.AppSpec, ghCtx *gha.GitHubContext) {
	repoOwner, repo := ghCtx.Repo()

	// Override app name to something that identifies this PR.
	spec.Name = generateAppName(repoOwner, repo, ghCtx.RefName)

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
		ref.Branch = ghCtx.HeadRef
	}
}

// generateAppName generates a unique app name based on the repoOwner, repo, and ref.
func generateAppName(repoOwner, repo, ref string) string {
	baseName := fmt.Sprintf("%s-%s", repoOwner, repo)
	baseName = strings.ToLower(baseName)
	baseName = strings.NewReplacer(
		"/", "-", // Replace slashes.
		":", "", // Colons are illegal.
		"_", "-", // Underscores are illegal.
	).Replace(baseName)

	// Generate a hash from the unique enumeration of repoOwner, repo, and ref.
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
