package main

import (
	gha "github.com/sethvargo/go-githubactions"
)

const (
	inputAppName = "app_name"
	inputToken   = "token"
)

func main() {
	doToken := gha.GetInput(inputToken)
	if doToken == "" {
		gha.Fatalf("missing input %q", inputToken)
	}
}
