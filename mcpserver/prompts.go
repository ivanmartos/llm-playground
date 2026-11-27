package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompts are templates the USER picks (in Claude Code: /mcp__hello-api__<name>).
// The server fills in the arguments and returns messages that start the
// conversation. Unlike tools, the model never calls them on its own.
//
// They are hand-written, not generated from the spec: they encode a workflow,
// which the spec can't describe. They refer to tools by name, so they break if
// those operations are renamed in the spec.
func registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "greet_team",
		Title:       "Greet a team",
		Description: "Greet several people with the Hello API and show the result as a table.",
		Arguments: []*mcp.PromptArgument{
			{Name: "names", Description: "Comma-separated names, e.g. Alice, Bob", Required: true},
			{Name: "version", Description: "API version to use: v1 or v2 (default v2)"},
		},
	}, greetTeamPrompt)

	server.AddPrompt(&mcp.Prompt{
		Name:        "compare_versions",
		Title:       "Compare API versions",
		Description: "Call v1 and v2 of the hello endpoint for one name and explain the differences.",
		Arguments: []*mcp.PromptArgument{
			{Name: "name", Description: "Name to greet", Required: true},
		},
	}, compareVersionsPrompt)
}

func greetTeamPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	var names []string
	for _, n := range strings.Split(req.Params.Arguments["names"], ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("argument %q is required", "names")
	}

	version := strings.ToLower(strings.TrimSpace(req.Params.Arguments["version"]))
	if version == "" {
		version = "v2"
	}
	tool := map[string]string{"v1": "helloV1", "v2": "helloV2"}[version]
	if tool == "" {
		return nil, fmt.Errorf("version must be v1 or v2, got %q", version)
	}

	text := fmt.Sprintf(
		"Greet each of these people using the hello-api tool `%s`, calling it once per name: %s.\n\n"+
			"Then show the results as a markdown table with the columns Name and Greeting. "+
			"If a call fails, put the error in the Greeting column instead.",
		tool, strings.Join(names, ", "))
	return userPrompt("Greet "+strings.Join(names, ", "), text), nil
}

func compareVersionsPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	name := strings.TrimSpace(req.Params.Arguments["name"])
	if name == "" {
		return nil, fmt.Errorf("argument %q is required", "name")
	}
	text := fmt.Sprintf(
		"Call the hello-api tools `helloV1` and `helloV2`, both with the name %q.\n\n"+
			"Show both raw JSON responses, then explain the differences between the two API versions: "+
			"field names, message text, extra fields and authentication. "+
			"You can read the resource hello-api://openapi.json for the documented schemas.",
		name)
	return userPrompt("Compare v1 and v2 for "+name, text), nil
}

func userPrompt(description, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: text}},
		},
	}
}
