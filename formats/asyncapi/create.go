package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"gopkg.in/yaml.v3"
)

const FormatToken = "asyncapi@^3.0.0"

// DefaultSourceName is the key used in the interface's Sources map for the AsyncAPI source.
const DefaultSourceName = "asyncapi"

func createInterfaceWithDoc(_ context.Context, in *openbindings.CreateInput, doc *Document) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	formatVersion := openbindings.DetectFormatVersion(doc.AsyncAPI)

	sourceEntry := openbindings.Source{
		Format: "asyncapi@" + formatVersion,
	}
	if src.Location != "" {
		sourceEntry.Location = src.Location
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         doc.Info.Title,
		Version:      doc.Info.Version,
		Description:  doc.Info.Description,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}

	usedKeys := map[string]bool{}

	opIDs := make([]string, 0, len(doc.Operations))
	for opID := range doc.Operations {
		opIDs = append(opIDs, opID)
	}
	sort.Strings(opIDs)

	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		opKey := openbindings.UniqueKey(openbindings.SanitizeKey(opID), usedKeys)
		usedKeys[opKey] = true

		obiOp := openbindings.Operation{
			Description: operationDescription(asyncOp),
		}

		if len(asyncOp.Tags) > 0 {
			for _, tag := range asyncOp.Tags {
				obiOp.Tags = append(obiOp.Tags, tag.Name)
			}
		}

		switch asyncOp.Action {
		case "receive":
			payload := resolveOperationPayload(doc, asyncOp)
			if payload != nil {
				obiOp.Output = payload
			}
		case "send":
			inputPayload := resolveOperationPayload(doc, asyncOp)
			if inputPayload != nil {
				obiOp.Input = inputPayload
			}
			if asyncOp.Reply != nil {
				outputPayload := resolveReplyPayload(doc, asyncOp.Reply)
				if outputPayload != nil {
					obiOp.Output = outputPayload
				}
			}
		}

		iface.Operations[opKey] = obiOp

		ref := "#/operations/" + opID
		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       ref,
		}
	}

	populateSecurity(doc, &iface)

	return &iface, nil
}

// populateSecurity reads the doc's security schemes and per-operation/server security
// requirements and populates iface.Security and each binding's Security field.
func populateSecurity(doc *Document, iface *openbindings.Interface) {
	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return
	}

	// Convert all security schemes to SecurityMethod.
	schemeMethods := map[string]openbindings.SecurityMethod{}
	for name, scheme := range doc.Components.SecuritySchemes {
		schemeMethods[name] = convertSecurityScheme(scheme)
	}
	if len(schemeMethods) == 0 {
		return
	}

	// Collect global security from servers (union of all server security requirements).
	var globalSchemeNames []string
	globalSeen := map[string]bool{}
	for _, server := range doc.Servers {
		for _, req := range server.Security {
			for schemeName := range req {
				if !globalSeen[schemeName] {
					globalSeen[schemeName] = true
					globalSchemeNames = append(globalSchemeNames, schemeName)
				}
			}
		}
	}
	sort.Strings(globalSchemeNames)

	securityEntries := map[string][]openbindings.SecurityMethod{}

	// Helper to build a security entry key and register it.
	buildSecEntry := func(schemeNames []string) string {
		if len(schemeNames) == 0 {
			return ""
		}
		sorted := make([]string, len(schemeNames))
		copy(sorted, schemeNames)
		sort.Strings(sorted)
		secKey := strings.Join(sorted, "+")

		if _, ok := securityEntries[secKey]; !ok {
			var methods []openbindings.SecurityMethod
			for _, name := range sorted {
				if m, ok := schemeMethods[name]; ok {
					methods = append(methods, m)
				}
			}
			if len(methods) > 0 {
				securityEntries[secKey] = methods
			}
		}
		return secKey
	}

	// For each operation, check operation-level security, then fall back to global.
	opIDs := make([]string, 0, len(doc.Operations))
	for opID := range doc.Operations {
		opIDs = append(opIDs, opID)
	}
	sort.Strings(opIDs)

	usedKeys := map[string]bool{}
	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		opKey := openbindings.UniqueKey(openbindings.SanitizeKey(opID), usedKeys)
		usedKeys[opKey] = true
		bindingKey := opKey + "." + DefaultSourceName

		var schemeNames []string
		if len(asyncOp.Security) > 0 {
			seen := map[string]bool{}
			for _, req := range asyncOp.Security {
				for schemeName := range req {
					if !seen[schemeName] {
						seen[schemeName] = true
						schemeNames = append(schemeNames, schemeName)
					}
				}
			}
		} else {
			schemeNames = globalSchemeNames
		}

		secKey := buildSecEntry(schemeNames)
		if secKey == "" {
			continue
		}

		if b, ok := iface.Bindings[bindingKey]; ok {
			b.Security = secKey
			iface.Bindings[bindingKey] = b
		}
	}

	if len(securityEntries) > 0 {
		iface.Security = securityEntries
	}
}

// convertSecurityScheme converts an AsyncAPI SecurityScheme to an OBI SecurityMethod.
func convertSecurityScheme(s SecurityScheme) openbindings.SecurityMethod {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "bearer":
			return openbindings.SecurityMethod{Type: "bearer", Description: s.Description}
		case "basic":
			return openbindings.SecurityMethod{Type: "basic", Description: s.Description}
		default:
			return openbindings.SecurityMethod{Type: s.Scheme, Description: s.Description}
		}
	case "apiKey":
		return openbindings.SecurityMethod{
			Type:        "apiKey",
			Description: s.Description,
			Name:        s.Name,
			In:          s.In,
		}
	case "oauth2":
		method := openbindings.SecurityMethod{Type: "oauth2", Description: s.Description}
		if s.Flows != nil {
			// Prefer authorizationCode, then implicit, clientCredentials, password.
			var flow *OAuthFlow
			if s.Flows.AuthorizationCode != nil {
				flow = s.Flows.AuthorizationCode
			} else if s.Flows.Implicit != nil {
				flow = s.Flows.Implicit
			} else if s.Flows.ClientCredentials != nil {
				flow = s.Flows.ClientCredentials
			} else if s.Flows.Password != nil {
				flow = s.Flows.Password
			}
			if flow != nil {
				method.AuthorizeURL = flow.AuthorizationURL
				method.TokenURL = flow.TokenURL
				if len(flow.Scopes) > 0 {
					scopes := make([]string, 0, len(flow.Scopes))
					for scope := range flow.Scopes {
						scopes = append(scopes, scope)
					}
					sort.Strings(scopes)
					method.Scopes = scopes
				}
			}
		}
		return method
	case "openIdConnect":
		return openbindings.SecurityMethod{Type: "bearer", Description: "OpenID Connect"}
	default:
		return openbindings.SecurityMethod{Type: s.Type, Description: s.Description}
	}
}

func loadDocument(ctx context.Context, client *http.Client, location string, content any) (*Document, error) {
	data, err := sourceToBytes(ctx, client, location, content)
	if err != nil {
		return nil, err
	}

	var doc Document

	if isJSON(data) {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse AsyncAPI JSON: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse AsyncAPI YAML: %w", err)
		}
	}

	if !strings.HasPrefix(doc.AsyncAPI, "3.") {
		return nil, fmt.Errorf("unsupported AsyncAPI version %q (expected 3.x)", doc.AsyncAPI)
	}

	resolveRefs(&doc)

	return &doc, nil
}

func sourceToBytes(ctx context.Context, client *http.Client, location string, content any) ([]byte, error) {
	if content != nil {
		return openbindings.ContentToBytes(content)
	}
	if location == "" {
		return nil, fmt.Errorf("source must have location or content")
	}
	if openbindings.IsHTTPURL(location) {
		req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", location, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", location, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("fetch %q: HTTP %d", location, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	}
	return os.ReadFile(location)
}

func isJSON(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func operationDescription(op Operation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func resolveOperationPayload(doc *Document, op Operation) map[string]any {
	if len(op.Messages) > 0 {
		msg := resolveMessageRef(doc, op.Messages[0])
		if msg != nil && msg.Payload != nil {
			return msg.Payload
		}
	}

	channelName := extractRefName(op.Channel.Ref)
	if channelName == "" {
		return nil
	}
	channel, ok := doc.Channels[channelName]
	if !ok {
		return nil
	}

	for _, msg := range channel.Messages {
		if msg.Payload != nil {
			return msg.Payload
		}
	}

	return nil
}

func resolveReplyPayload(doc *Document, reply *OperationReply) map[string]any {
	if reply == nil {
		return nil
	}

	if len(reply.Messages) > 0 {
		msg := resolveMessageRef(doc, reply.Messages[0])
		if msg != nil && msg.Payload != nil {
			return msg.Payload
		}
	}

	return nil
}

func resolveMessageRef(doc *Document, ref MessageRef) *Message {
	if ref.Ref == "" {
		return nil
	}

	path := strings.TrimPrefix(ref.Ref, "#/")
	parts := strings.Split(path, "/")

	if len(parts) == 3 && parts[0] == "components" && parts[1] == "messages" {
		if doc.Components != nil {
			if msg, ok := doc.Components.Messages[parts[2]]; ok {
				return &msg
			}
		}
	}

	if len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages" {
		if ch, ok := doc.Channels[parts[1]]; ok {
			if msg, ok := ch.Messages[parts[3]]; ok {
				return &msg
			}
		}
	}

	return nil
}

func extractRefName(ref string) string {
	if ref == "" {
		return ""
	}
	path := strings.TrimPrefix(ref, "#/")
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
