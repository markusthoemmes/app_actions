# app_actions

This is a complete rewrite of [`app_action`](https://github.com/digitalocean/app_action) with the goal of being more orchestratable in a broader GitHub Actions context.

## Usage

### Deploy an app after an image build

```yaml
TODO
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
      - name: checkout repo
        uses: actions/checkout@v4
      - name: deploy the app
        id: deploy
        uses: markusthoemmes/app_action_v2@main
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

## Changes from v1

### Breaking Changes

- The `images` input is no longer supported. Instead, use env-var-substitution for an in-repository app spec or the `IMAGE_DIGEST_$component-name`/`IMAGE_TAG_$component-name` environment variables to change the respective fields of images in an app.

### Other changes

- Rewritten to use `godo` instead of shelling out to `doctl` for better error handing and overall control of the process.
- Supports picking up an in-repository (or filesystem really) `app.yaml` (defaults to `.do/app.yaml`).
- Prints the build and deploy logs into the Github Action log and surfaces them as outputs (fixes https://github.com/digitalocean/app_action/issues/73).
- Provides the app's metadata as an output (fixes https://github.com/digitalocean/app_action/issues/92).
- Supports a "preview mode" geared towards orchestrating per-PR app previews.