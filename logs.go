package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/digitalocean/godo"
	gha "github.com/sethvargo/go-githubactions"
)

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

func printLogs(a *gha.Action, logs []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(logs))
	for scanner.Scan() {
		a.Infof(scanner.Text())
	}
}
