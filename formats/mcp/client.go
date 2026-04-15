package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// discovery holds the results of an MCP server's capability negotiation.
type discovery struct {
	Tools             []*gomcp.Tool
	Resources         []*gomcp.Resource
	ResourceTemplates []*gomcp.ResourceTemplate
	Prompts           []*gomcp.Prompt
	ServerInfo        *gomcp.Implementation
}

func clientInfo(version string) *gomcp.Implementation {
	return &gomcp.Implementation{
		Name:    "ob",
		Version: version,
	}
}

func discover(ctx context.Context, clientVersion string, url string) (*discovery, error) {
	session, err := connect(ctx, clientVersion, url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer func() { _ = session.Close() }()

	result := &discovery{}

	initResult := session.InitializeResult()
	if initResult != nil {
		result.ServerInfo = initResult.ServerInfo
	}

	if initResult != nil && initResult.Capabilities.Tools != nil {
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list tools: %w", err)
			}
			result.Tools = append(result.Tools, tool)
		}
	}

	if initResult != nil && initResult.Capabilities.Resources != nil {
		for resource, err := range session.Resources(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resources: %w", err)
			}
			result.Resources = append(result.Resources, resource)
		}

		for tmpl, err := range session.ResourceTemplates(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resource templates: %w", err)
			}
			result.ResourceTemplates = append(result.ResourceTemplates, tmpl)
		}
	}

	if initResult != nil && initResult.Capabilities.Prompts != nil {
		for prompt, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list prompts: %w", err)
			}
			result.Prompts = append(result.Prompts, prompt)
		}
	}

	return result, nil
}

// readResourcePooled reads a resource using a pooled session.
func readResourcePooled(ctx context.Context, pool *sessionPool, clientVersion string, url string, uri string, headers map[string]string) (*gomcp.ReadResourceResult, error) {
	s, err := pool.acquire(ctx, clientVersion, url, headers)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer s.release()

	result, err := s.session.ReadResource(ctx, &gomcp.ReadResourceParams{
		URI: uri,
	})
	if err != nil {
		return nil, fmt.Errorf("read resource %q: %w", uri, err)
	}
	return result, nil
}

// getPromptPooled gets a prompt using a pooled session.
func getPromptPooled(ctx context.Context, pool *sessionPool, clientVersion string, url string, promptName string, args map[string]string, headers map[string]string) (*gomcp.GetPromptResult, error) {
	s, err := pool.acquire(ctx, clientVersion, url, headers)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	defer s.release()

	result, err := s.session.GetPrompt(ctx, &gomcp.GetPromptParams{
		Name:      promptName,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("get prompt %q: %w", promptName, err)
	}
	return result, nil
}

func connect(ctx context.Context, clientVersion string, url string, headers map[string]string) (*gomcp.ClientSession, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("MCP source location must be an HTTP or HTTPS URL, got %q", url)
	}

	transport := &gomcp.StreamableClientTransport{Endpoint: url}
	if len(headers) > 0 {
		transport.HTTPClient = &http.Client{
			Transport: &headerTransport{
				base:    http.DefaultTransport,
				headers: headers,
			},
		}
	}

	client := gomcp.NewClient(clientInfo(clientVersion), nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// headerTransport injects extra HTTP headers into every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		if clone.Header.Get(k) == "" {
			clone.Header.Set(k, v)
		}
	}
	return t.base.RoundTrip(clone)
}
