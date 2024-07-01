package main

import (
	"fmt"
	"strconv"

	gha "github.com/sethvargo/go-githubactions"
)

// inputs are the inputs for the action.
type inputs struct {
	token           string
	appName         string
	printBuildLogs  bool
	printDeployLogs bool
	deployPRPreview bool
}

// getInputs gets the inputs for the action.
func getInputs(a *gha.Action) (inputs, error) {
	var in inputs
	for _, err := range []error{
		inputAsString(a, "token", &in.token),
		inputAsString(a, "app_name", &in.appName),
		inputAsBool(a, "print_build_logs", &in.printBuildLogs),
		inputAsBool(a, "print_deploy_logs", &in.printDeployLogs),
		inputAsBool(a, "deploy_pr_preview", &in.deployPRPreview),
	} {
		if err != nil {
			return in, err
		}
	}
	return in, nil
}

// inputAsString parses the input as a string and sets the target.
func inputAsString(a *gha.Action, input string, target *string) error {
	str := a.GetInput(input)
	if str == "" {
		return fmt.Errorf("input %q is required", input)
	}
	*target = str
	return nil
}

// inputAsBool parses the input as a boolean and sets the target.
func inputAsBool(a *gha.Action, input string, target *bool) error {
	str := a.GetInput(input)
	if str == "" {
		return fmt.Errorf("input %q is required", input)
	}
	val, err := strconv.ParseBool(str)
	if err != nil {
		return fmt.Errorf("failed to parse %q as a boolean: %v", input, err)
	}
	*target = val
	return nil
}
