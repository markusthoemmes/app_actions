# app_actions

This is a complete rewrite of [`app_action`](https://github.com/digitalocean/app_action) with the goal of being more orchestratable in a broader GitHub Actions context.

## Changes from v1

### Breaking Changes

- The `images` input is no longer supported. Instead, use env-var-substitution for an in-repository app spec or the `IMAGE_DIGEST_$component-name`/`IMAGE_TAG_$component-name` environment variables to change the respective fields of images in an existing app.

### Other changes

- Rewritten to use `godo` instead of shelling out to `doctl` for better error handling and overall control of the process.
- Supports picking up an in-repository (or filesystem really) `app.yaml` (defaults to `.do/app.yaml`, configurable via the `app_spec_location` input) to create the app from instead of having to rely on an already existing app that's then downloaded (though that is still supported). The in-filesystem app spec can also be templated with environment variables automatically (see examples below) (fixes https://github.com/digitalocean/app_action/issues/106).
- Prints the build and deploy logs into the Github Action log (configurable via `print_build_logs` and `print_deploy_logs`) and surfaces them as outputs `build_logs` and `deploy_logs` (fixes https://github.com/digitalocean/app_action/issues/73).
- Provides the app's metadata as the output `app` (fixes https://github.com/digitalocean/app_action/issues/92).
- Supports a "preview mode" geared towards orchestrating per-PR app previews. It can be enabled via `deploy_pr_review`, see the [Implementing Preview Apps](#implementing-preview-apps) example.

## Usage

### Deploy an app after an image build

With the following contents of `.do/app.yaml` in the repository:

```yaml
name: sample
services:
- name: sample
  image:
    registry_type: GHCR
    registry: markusthoemmes
    repository: app_action_example
    digest: ${FOOBAR_DIGEST}
```

```yaml
name: Build, Push and Deploy a Docker Image

on:
  pull_request:
    branches: [main]

jobs:
  build-push-deploy-image:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
      id-token: write
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
      - name: Log in to the Container registry
        uses: docker/login-action@65b78e6e13532edd9afa3aa52ac7964289d1a9c1
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Build and push Docker image
        id: push
        uses: docker/build-push-action@f2a1d5e99d037542a71f64918e516c093c6f3fc4
        with:
          context: .
          push: true
          tags: ghcr.io/${{ github.repository }}:latest
      - name: Deploy the app
        id: deploy
        uses: markusthoemmes/app_actions/deploy@main
        env:
          FOOBAR_DIGEST: ${{ steps.push.outputs.digest }}
        with:
          deploy_pr_preview: "true"
          token: ${{ secrets.DIGITALOCEAN_ACCESS_TOKEN }}
```

### Implementing Preview Apps

```yaml
name: App Platform Preview

on:
  pull_request:
    branches: [main]

permissions:
  pull-requests: write

jobs:
  test:
    name: preview
    runs-on: ubuntu-latest
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
      - name: Deploy the app
        id: deploy
        uses: markusthoemmes/app_actions/deploy@main
        with:
          deploy_pr_preview: "true"
          token: ${{ secrets.DIGITALOCEAN_ACCESS_TOKEN }}
      - uses: actions/github-script@v7
        env:
          BUILD_LOGS: ${{ steps.deploy.outputs.build_logs }}
          DEPLOY_LOGS: ${{ steps.deploy.outputs.deploy_logs }}
        with:
          script: |
            const { BUILD_LOGS, DEPLOY_LOGS } = process.env
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo: context.repo.repo,
              body: `:rocket: :rocket: :rocket: The app was successfully deployed at ${{ fromJson(steps.deploy.outputs.app).live_url }}.

              ## Logs
              <details>
              <summary>Build logs</summary>

              \`\`\`
              ${BUILD_LOGS}
              \`\`\`
              </details>

              <details>
              <summary>Deploy logs</summary>

              \`\`\`
              ${DEPLOY_LOGS}
              \`\`\`
              </details>`
            })
      - uses: actions/github-script@v7
        if: failure()
        with:
          script: |
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo: context.repo.repo,
              body: 'The app failed to be deployed. Logs can be found [here](https://github.com/${{ github.repository }}/actions/runs/${{ github.run_id }}).'
            })
```
