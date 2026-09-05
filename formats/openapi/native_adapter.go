package openapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// runNative is the complete OpenBindings lifecycle translation over the
// supported standalone client surface. It owns no OpenAPI request, security,
// target, response, or stream mechanics.
func (e *Runtime) runNative(ctx context.Context, args *invoke.BindingInvocationArgs, outer *invoke.InvocationImpl[any, any]) error {
	if err := assertNativeBindingSpec(args); err != nil {
		return err
	}
	client, err := e.loadNativeClient(ctx, args, true)
	if err != nil {
		return nativeInvocationError(err)
	}
	if err := acceptedNativeEdition(args.Source.BindingSpec, client.Edition()); err != nil {
		return err
	}
	if _, err := client.Operation(openapiclient.OperationRef(args.Selector)); err != nil {
		return nativeSelectionInvocationError(args, err)
	}
	options, err := e.nativeCallOptions(args)
	if err != nil {
		return err
	}
	configured, err := nativeConfiguredInput(args.Context)
	if err != nil {
		return err
	}
	credentialNames, err := nativeCredentialNames(ctx, client, args.Selector, configured, options)
	if err != nil {
		return nativeSelectionInvocationError(args, err)
	}
	options.Auth, err = e.nativeCredentials(args.Context, credentialNames, options.Auth)
	if err != nil {
		return err
	}
	requirements, err := client.Preflight(ctx, openapiclient.OperationRef(args.Selector), configured, options)
	if err != nil {
		return nativeSelectionInvocationError(args, err)
	}
	if details, err := nativeBindingRequirements(requirements); err != nil {
		return err
	} else if details != nil {
		return invoke.NewContextRequiredError(details)
	}
	operation, err := client.AnalyzeOperation(openapiclient.OperationRef(args.Selector))
	if err != nil {
		return nativeSelectionInvocationError(args, err)
	}

	bridgeCtx, stop := invoke.DoneContext(ctx, outer.Done())
	defer stop()
	var value any
	var readErr error
	if len(operation.Parameters) == 0 && len(operation.RequestBodies) == 0 {
		// A zero-input operation starts without waiting for the caller, but a
		// value queued before analysis completed is retained and validated.
		_ = outer.CloseInput()
		value, readErr = outer.ReadInput(bridgeCtx)
	} else {
		value, readErr = outer.ReadInput(bridgeCtx)
		_ = outer.CloseInput()
	}
	input := configured
	switch {
	case errors.Is(readErr, io.EOF):
	case readErr != nil:
		return readErr
	default:
		input, err = nativeBindingInput(operation, value, configured, client.Edition())
		if err != nil {
			return err
		}
	}
	result, err := client.Stream(bridgeCtx, openapiclient.OperationRef(args.Selector), input, options)
	if err != nil {
		if bridgeCtx.Err() != nil {
			return nil
		}
		return nativeInvocationError(err)
	}
	if !result.OK {
		if result.OpenAPI.Declared && result.OpenAPI.MediaType != "" && result.Error != nil {
			return invoke.NewInvocationErrorWithData(invoke.ErrCodeExecutionFailed, nativePortableValue(result.Error))
		}
		return invoke.NewInvocationError(invoke.ErrCodeExecutionFailed)
	}
	if result.Stream == nil {
		outer.CloseOutput()
		return nil
	}
	for {
		event, open, streamErr := result.Stream.Next(bridgeCtx)
		if streamErr != nil {
			if bridgeCtx.Err() != nil {
				result.Stream.Cancel()
				return nil
			}
			return nativeInvocationError(streamErr)
		}
		if !open {
			break
		}
		output := nativePortableValue(event.Data)
		if event.SSE != nil && args.Source.BindingSpec != BindingSpecOpenAPI32 {
			frame := map[string]any{"data": output}
			if event.SSE.Event != "" {
				frame["event"] = event.SSE.Event
			}
			if event.SSE.ID != "" {
				frame["id"] = event.SSE.ID
			}
			if event.SSE.Retry != nil {
				frame["retry"] = *event.SSE.Retry
			}
			output = frame
		}
		if emitErr := outer.EmitOutput(output); emitErr != nil {
			result.Stream.Cancel()
			return nil
		}
	}
	if err := result.Stream.Wait(); err != nil {
		if bridgeCtx.Err() != nil {
			return nil
		}
		return nativeInvocationError(err)
	}
	outer.CloseOutput()
	return nil
}

func (e *Runtime) prepareNativeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	if err := assertNativeBindingSpec(args); err != nil {
		return nil, err
	}
	// The Core preflight is explicitly side-effect-free. An inline source can
	// be analyzed locally; a location-only source remains unknown until the
	// authoritative invocation load.
	if args.Source.Content == nil {
		if _, present := e.cachedNativeClient(args); !present {
			return nil, nil
		}
	}
	client, err := e.loadNativeClient(ctx, args, false)
	if err != nil {
		mapped := nativeInvocationError(err)
		if mapped.Code == invoke.ErrCodeRefused {
			return nil, mapped
		}
		return nil, nil
	}
	if err := acceptedNativeEdition(args.Source.BindingSpec, client.Edition()); err != nil {
		return nil, nil
	}
	if _, err := client.Operation(openapiclient.OperationRef(args.Selector)); err != nil {
		mapped := nativeSelectionInvocationError(args, err)
		if mapped.Code == invoke.ErrCodeRefused {
			return nil, mapped
		}
		return nil, nil
	}
	options, err := e.nativeCallOptions(args)
	if err != nil {
		return nil, err
	}
	configured, err := nativeConfiguredInput(args.Context)
	if err != nil {
		return nil, err
	}
	names, err := nativeCredentialNames(ctx, client, args.Selector, configured, options)
	if err != nil {
		mapped := nativeSelectionInvocationError(args, err)
		if mapped.Code == invoke.ErrCodeRefused {
			return nil, mapped
		}
		return nil, nil
	}
	options.Auth, err = e.nativeCredentials(args.Context, names, options.Auth)
	if err != nil {
		return nil, err
	}
	requirements, err := client.Preflight(ctx, openapiclient.OperationRef(args.Selector), configured, options)
	if err != nil {
		mapped := nativeSelectionInvocationError(args, err)
		if mapped.Code == invoke.ErrCodeRefused {
			return nil, mapped
		}
		return nil, nil
	}
	return nativeBindingRequirements(requirements)
}

func (e *Runtime) loadNativeClient(ctx context.Context, args *invoke.BindingInvocationArgs, allowDocumentFetch bool) (*openapiclient.Client, error) {
	if args == nil {
		return nil, &openapiclient.ClientError{Kind: openapiclient.ErrorSource, Code: "SOURCE_LOAD_FAILED", Message: "OpenAPI invocation arguments are nil"}
	}
	if cached, present := e.cachedNativeClient(args); present {
		return cached, nil
	}
	var content []byte
	var err error
	if args.Source.Content != nil {
		content, err = openbindings.ContentToBytes(args.Source.Content)
		if err != nil {
			return nil, err
		}
	}
	documentClient := e.client
	if !allowDocumentFetch {
		documentClient = &http.Client{Transport: nativeNoDocumentFetchTransport{}}
	}
	client, err := openapiclient.Load(ctx, openapiclient.Source{Location: args.Source.Location, Content: content}, openapiclient.Options{
		DocumentHTTPClient:         documentClient,
		HTTPClient:                 e.client,
		Auth:                       e.nativeHandlerCredentials(),
		Redirect:                   e.redirect,
		ParameterConverter:         openapiclient.ParameterConverter(e.parameterConvert),
		RequestContentCodings:      nativeContentEncoders(e.requestCodings),
		ResponseContentCodings:     nativeContentDecoders(e.responseCodings),
		RequestCharacterEncodings:  nativeCharacterEncoders(e.requestCharacters),
		ResponseCharacterEncodings: nativeCharacterDecoders(e.responseCharacters),
	})
	if err != nil {
		return nil, err
	}
	if key := nativeLocationClientKey(args); key != "" {
		e.nativeClientsMu.Lock()
		if present := e.nativeClients[key]; present != nil {
			client = present
		} else {
			e.nativeClients[key] = client
		}
		e.nativeClientsMu.Unlock()
	}
	return client, nil
}

type nativeNoDocumentFetchTransport struct{}

func (nativeNoDocumentFetchTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("PrepareBinding does not retrieve external OpenAPI resources")
}

func assertNativeBindingSpec(args *invoke.BindingInvocationArgs) *invoke.InvocationError {
	if args == nil {
		return invoke.NewInvocationError(invoke.ErrCodeSourceConfigError)
	}
	if _, supported := openAPIBindingSpecRegistry[args.Source.BindingSpec]; !supported {
		return unsupportedBindingSpecError(args.Source.BindingSpec)
	}
	return nil
}

func nativeLocationClientKey(args *invoke.BindingInvocationArgs) string {
	if args == nil || args.Source.Location == "" {
		return ""
	}
	return args.Source.BindingSpec + "\x00" + args.Source.Location
}

func (e *Runtime) cachedNativeClient(args *invoke.BindingInvocationArgs) (*openapiclient.Client, bool) {
	key := nativeLocationClientKey(args)
	if key == "" {
		return nil, false
	}
	e.nativeClientsMu.RLock()
	client, present := e.nativeClients[key]
	e.nativeClientsMu.RUnlock()
	return client, present
}

func acceptedNativeEdition(bindingSpec string, edition openapiclient.Edition) *invoke.InvocationError {
	if _, supported := openAPIBindingSpecRegistry[bindingSpec]; !supported {
		return unsupportedBindingSpecError(bindingSpec)
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpecValue(string(edition), bindingSpec); err != nil {
		return invoke.NewInvocationError(invoke.ErrCodeSourceLoadFailed)
	}
	return nil
}

func checkAcceptedOpenAPIVersionForBindingSpecValue(edition, bindingSpec string) error {
	registration, ok := openAPIBindingSpecRegistry[bindingSpec]
	if !ok || !registration.editions[edition] {
		return fmt.Errorf("edition %q is not admitted by %q", edition, bindingSpec)
	}
	return nil
}

func (e *Runtime) nativeCallOptions(args *invoke.BindingInvocationArgs) (openapiclient.CallOptions, error) {
	configuration := invoke.ContextConfiguration(args.Context)
	server, err := nativeServerSelection(configuration)
	if err != nil {
		return openapiclient.CallOptions{}, err
	}
	security, err := nativeSecurityAlternative(configuration)
	if err != nil {
		return openapiclient.CallOptions{}, err
	}
	implicitScope := ""
	if value, present := configuration["implicitConnectionScope"]; present {
		implicitScope, _ = value.(string)
		if implicitScope != "entry" && implicitScope != "referring" {
			return openapiclient.CallOptions{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	emptyValueForm := ""
	if value, present := configuration["emptyValueForm"]; present {
		emptyValueForm, _ = value.(string)
		if emptyValueForm != "name-only" && emptyValueForm != "empty" {
			return openapiclient.CallOptions{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	return openapiclient.CallOptions{
		HTTPClient:                 e.client,
		Auth:                       e.nativeHandlerCredentials(),
		Server:                     server,
		SecurityAlternative:        security,
		ImplicitConnectionScope:    openapiclient.ImplicitConnectionScope(implicitScope),
		EmptyValueForm:             openapiclient.EmptyValueForm(emptyValueForm),
		MaxDeliveryUnitBytes:       args.MaxDeliveryUnitBytes,
		ParameterConverter:         openapiclient.ParameterConverter(e.parameterConvert),
		RequestContentCodings:      nativeContentEncoders(e.requestCodings),
		ResponseContentCodings:     nativeContentDecoders(e.responseCodings),
		RequestCharacterEncodings:  nativeCharacterEncoders(e.requestCharacters),
		ResponseCharacterEncodings: nativeCharacterDecoders(e.responseCharacters),
		Hooks:                      bridgeHooks(args, args.Source.BindingSpec),
	}, nil
}

func nativeServerSelection(configuration map[string]any) (openapiclient.ServerSelection, error) {
	value, present := configuration["server"]
	if !present {
		return nil, nil
	}
	if url, ok := value.(string); ok && url != "" {
		return openapiclient.ServerURL(url), nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	variables, variablesPresent, err := nativeStringMap(object["variables"])
	if err != nil {
		return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	if base, ok := object["baseUrl"].(string); ok && base != "" {
		if len(object) != 1 {
			return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
		return openapiclient.ServerURL(base), nil
	}
	if raw, exists := object["index"]; exists {
		index, valid := nativeConfigIndex(raw)
		if !valid || index < 0 || len(object) > 2 || len(object) == 2 && !variablesPresent {
			return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
		return openapiclient.Server(index, variables), nil
	}
	if variablesPresent && len(object) == 1 {
		return openapiclient.ServerVariables(variables), nil
	}
	return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
}

func nativeSecurityAlternative(configuration map[string]any) (*int, error) {
	value, present := configuration["security"]
	if !present {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	index, valid := nativeConfigIndex(object["index"])
	if !valid || index < 0 {
		return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	return &index, nil
}

func nativeConfiguredInput(bindCtx map[string]any) (openapiclient.Input, error) {
	configuration := invoke.ContextConfiguration(bindCtx)
	result := openapiclient.Input{}
	if value, present := configuration["requestMedia"]; present {
		result.MediaType, _ = value.(string)
		if result.MediaType == "" {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	if value, present := configuration["propertyMedia"]; present {
		members, _, err := nativeStringMap(value)
		if err != nil {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
		result.PropertyMediaTypes = members
	}
	return result, nil
}

func nativeStringMap(value any) (map[string]string, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		if typed, typedOK := value.(map[string]string); typedOK {
			copy := make(map[string]string, len(typed))
			for name, member := range typed {
				copy[name] = member
			}
			return copy, true, nil
		}
		return nil, true, fmt.Errorf("value is not an object of strings")
	}
	result := make(map[string]string, len(object))
	for name, member := range object {
		text, ok := member.(string)
		if !ok {
			return nil, true, fmt.Errorf("member %q is not a string", name)
		}
		result[name] = text
	}
	return result, true, nil
}

func nativeConfigIndex(raw any) (int, bool) {
	if number, ok := raw.(json.Number); ok {
		integer, err := number.Int64()
		return int(integer), err == nil && int64(int(integer)) == integer
	}
	switch number := raw.(type) {
	case int:
		return number, true
	case float64:
		if number == float64(int(number)) {
			return int(number), true
		}
	}
	return 0, false
}

func nativeCredentialNames(ctx context.Context, client *openapiclient.Client, selector string, input openapiclient.Input, options openapiclient.CallOptions) (map[string]string, error) {
	requirements, err := client.Preflight(ctx, openapiclient.OperationRef(selector), input, options)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, alternative := range requirementsAlternatives(requirements) {
		for _, requirement := range alternative.Requirements {
			if requirement.Kind == openapiclient.RequirementCredential {
				result[requirement.Name] = requirement.Credential
			}
		}
	}
	return result, nil
}

func requirementsAlternatives(requirements *openapiclient.ConfigurationRequirements) []openapiclient.ConfigurationAlternative {
	if requirements == nil {
		return nil
	}
	return requirements.Alternatives
}

func (e *Runtime) nativeHandlerCredentials() openapiclient.Credentials {
	if len(e.securityHandlers) == 0 {
		return nil
	}
	result := make(openapiclient.Credentials, len(e.securityHandlers))
	for name, handler := range e.securityHandlers {
		if handler != nil {
			result[name] = openapiclient.CustomSecurity(handler)
		}
	}
	return result
}

func (e *Runtime) nativeCredentials(bindCtx map[string]any, selected map[string]string, base openapiclient.Credentials) (openapiclient.Credentials, error) {
	credentials, _ := nativeAnyMap(bindCtx["credentials"])
	apiKeys, _ := nativeAnyMap(bindCtx["apiKeys"])
	result := make(openapiclient.Credentials, len(base)+len(selected))
	for name, credential := range base {
		result[name] = credential
	}
	for name, kind := range selected {
		value, present := credentials[name]
		if !present {
			value, present = apiKeys[name]
		}
		if !present {
			value, present = nativeFlatCredential(bindCtx, kind)
		}
		if !present {
			continue
		}
		if stringsMap, ok := value.(map[string]string); ok {
			object := make(map[string]any, len(stringsMap))
			for key, member := range stringsMap {
				object[key] = member
			}
			value = object
		}
		switch typed := value.(type) {
		case string:
			result[name] = openapiclient.Token(typed)
		case map[string]any:
			username, usernameOK := typed["userId"].(string)
			if !usernameOK {
				username, usernameOK = typed["username"].(string)
			}
			if password, ok := typed["password"].(string); ok && usernameOK {
				result[name] = openapiclient.Basic(username, password)
				continue
			}
			if token, ok := typed["accessToken"].(string); ok {
				if tokenType, exists := typed["tokenType"]; exists && tokenType != "Bearer" {
					return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
				}
				result[name] = openapiclient.Token(token)
				continue
			}
			return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
		default:
			return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	return result, nil
}

func nativeAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		result := make(map[string]any, len(typed))
		for name, member := range typed {
			result[name] = member
		}
		return result, true
	default:
		return nil, false
	}
}

func nativeFlatCredential(bindCtx map[string]any, kind string) (any, bool) {
	if bindCtx == nil {
		return nil, false
	}
	var names []string
	switch kind {
	case "apiKey":
		names = []string{"apiKey"}
	case "basic":
		names = []string{"basic"}
	case "bearer":
		names = []string{"bearerToken"}
	case "oauth2":
		names = []string{"accessToken", "bearerToken"}
	default:
		return nil, false
	}
	for _, name := range names {
		if value, present := bindCtx[name]; present {
			return value, true
		}
	}
	return nil, false
}

func nativeBindingInput(operation openapiclient.OperationAnalysis, value any, configured openapiclient.Input, edition openapiclient.Edition) (openapiclient.Input, error) {
	envelope, ok := value.(map[string]any)
	if !ok {
		return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	for name := range envelope {
		if name != "parameters" && name != "body" {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	flat := map[string]any{}
	if raw, present := envelope["parameters"]; present {
		flat, ok = raw.(map[string]any)
		if !ok {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	result := configured
	claimed := map[string]bool{}
	form := map[string]any{}
	for _, parameter := range operation.Parameters {
		member, present := flat[parameter.InputKey]
		if !present || parameter.In == "body" {
			continue
		}
		claimed[parameter.InputKey] = true
		if parameter.In == "formData" {
			form[parameter.Name] = member
			continue
		}
		destination := nativeParameterMap(&result.Parameters, parameter.In)
		if destination == nil {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
		destination[parameter.Name] = member
	}
	for name := range flat {
		if !claimed[name] {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
	}
	body, bodyPresent := envelope["body"]
	if len(form) > 0 {
		if bodyPresent {
			return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
		}
		body, bodyPresent = form, true
	}
	if bodyPresent && edition != openapiclient.Swagger20 {
		if analysis, selected := nativeSelectedBodyAnalysis(operation, configured.MediaType); selected {
			if analysis.Base64 {
				decoded, decodeErr := nativeCanonicalBase64(body)
				if decodeErr != nil {
					return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
				}
				body = decoded
			} else if len(analysis.Base64Properties) > 0 {
				object, objectOK := body.(map[string]any)
				if !objectOK {
					return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
				}
				copy := make(map[string]any, len(object))
				for name, member := range object {
					copy[name] = member
				}
				for _, name := range analysis.Base64Properties {
					member, present := copy[name]
					if !present {
						continue
					}
					encoded, encodeErr := nativeBoundaryBase64(member)
					if encodeErr != nil {
						return openapiclient.Input{}, invoke.NewInvocationError(invoke.ErrCodeRefused)
					}
					copy[name] = encoded
				}
				body = copy
			}
		}
	}
	result.Body, result.BodyPresent = body, bodyPresent
	return result, nil
}

func nativeSelectedBodyAnalysis(operation openapiclient.OperationAnalysis, selected string) (openapiclient.RequestBodyInfo, bool) {
	usable := operation.RequestBodies
	if selected != "" {
		usable = nil
		for _, body := range operation.RequestBodies {
			if nativeMediaIdentity(body.MediaType) == nativeMediaIdentity(selected) {
				usable = append(usable, body)
			}
		}
	}
	if len(usable) != 1 {
		return openapiclient.RequestBodyInfo{}, false
	}
	return usable[0], true
}

func nativeCanonicalBase64(value any) ([]byte, error) {
	if bytes, ok := value.([]byte); ok {
		return append([]byte(nil), bytes...), nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("canonical Base64 value must be a string")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != text {
		return nil, fmt.Errorf("value is not canonical Base64")
	}
	return decoded, nil
}

func nativeBoundaryBase64(value any) (string, error) {
	if bytes, ok := value.([]byte); ok {
		return base64.StdEncoding.EncodeToString(bytes), nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("Base64 boundary value must be a string or byte slice")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != text {
		return "", fmt.Errorf("value is not canonical Base64")
	}
	return text, nil
}

func nativeMediaIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
}

func nativeParameterMap(parameters *openapiclient.Parameters, location string) map[string]any {
	var target *map[string]any
	switch location {
	case "path":
		target = &parameters.Path
	case "query":
		target = &parameters.Query
	case "querystring":
		target = &parameters.QueryString
	case "header":
		target = &parameters.Header
	case "cookie":
		target = &parameters.Cookie
	default:
		return nil
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return *target
}

func nativeBindingRequirements(requirements *openapiclient.ConfigurationRequirements) (*invoke.ContextRequiredDetails, error) {
	if requirements == nil {
		return nil, nil
	}
	capabilities := map[string]bool{
		"ParameterConverter": true, "RequestContentCodings": true, "ResponseContentCodings": true,
		"RequestCharacterEncodings": true, "ResponseCharacterEncodings": true,
	}
	result := &invoke.ContextRequiredDetails{Target: requirements.Target, Alternatives: make([]invoke.ContextAlternative, len(requirements.Alternatives))}
	for index, alternative := range requirements.Alternatives {
		for _, requirement := range alternative.Requirements {
			if requirement.Kind == openapiclient.RequirementOption && capabilities[requirement.Name] {
				return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
			}
			durable := true
			translated := invoke.ContextRequirement{Name: requirement.Name, Description: requirement.Description, Durable: &durable}
			switch requirement.Kind {
			case openapiclient.RequirementCredential:
				translated.Type = "auth." + requirement.Credential
				translated.Extra = cloneNativeDetails(requirement.Details)
			case openapiclient.RequirementInput, openapiclient.RequirementOption:
				translated.Type = "config.value"
				translated.Extra = map[string]any{
					"point": nativeContextPoint(requirement),
					"path":  requirement.Path,
				}
				if len(requirement.AllowedValues) > 0 {
					translated.Extra["schema"] = map[string]any{"enum": requirement.AllowedValues}
				}
			default:
				return nil, invoke.NewInvocationError(invoke.ErrCodeRefused)
			}
			result.Alternatives[index].Requirements = append(result.Alternatives[index].Requirements, translated)
		}
	}
	return result, nil
}

func cloneNativeDetails(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil
	}
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func nativeContextPoint(requirement openapiclient.ConfigurationRequirement) string {
	if requirement.Kind == openapiclient.RequirementInput {
		if requirement.Name == "MediaType" {
			return "requestMedia"
		}
		if requirement.Name == "PropertyMediaTypes" {
			return "propertyMedia"
		}
	}
	switch requirement.Name {
	case "SecurityAlternative":
		return "security"
	case "ParameterConverter":
		return "parameterConversion"
	case "Server":
		return "server"
	case "EmptyValueForm":
		return "emptyValueForm"
	case "RequestContentCodings":
		return "requestContentCodings"
	case "ResponseContentCodings":
		return "responseContentCodings"
	case "RequestCharacterEncodings":
		return "requestCharacterEncodings"
	case "ResponseCharacterEncodings":
		return "responseCharacterEncodings"
	default:
		return requirement.Name
	}
}

func nativePortableValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return base64.StdEncoding.EncodeToString(typed)
	default:
		return value
	}
}

func nativeInvocationError(err error) *invoke.InvocationError {
	var authored *invoke.InvocationError
	if errors.As(err, &authored) {
		return authored
	}
	var client *openapiclient.ClientError
	if !errors.As(err, &client) {
		return invoke.NewInvocationError(invoke.ErrCodeRuntime)
	}
	switch {
	case client.Code == openapiclient.CodeConfigurationRequired && client.Requirements != nil:
		details, mappingErr := nativeBindingRequirements(client.Requirements)
		if mappingErr != nil {
			return invoke.AsInvocationError(mappingErr)
		}
		return invoke.NewContextRequiredError(details)
	case client.Kind == openapiclient.ErrorSource:
		return invoke.NewInvocationError(invoke.ErrCodeSourceLoadFailed)
	case client.Code == "SOURCE_EXCLUDED":
		return invoke.NewInvocationError(invoke.ErrCodeSelectorNotFound)
	case client.Code == "INVALID_OPERATION_REF":
		return invoke.NewInvocationError(invoke.ErrCodeInvalidSelector)
	case client.Code == "OPERATION_NOT_FOUND":
		return invoke.NewInvocationError(invoke.ErrCodeSelectorNotFound)
	case client.Code == invoke.ErrCodeConnectFailed:
		return invoke.NewInvocationError(invoke.ErrCodeConnectFailed)
	case client.Code == invoke.ErrCodeTimeout:
		return invoke.NewInvocationError(invoke.ErrCodeTimeout)
	case client.Code == invoke.ErrCodeCancelled:
		return invoke.NewInvocationError(invoke.ErrCodeCancelled)
	case client.Code == invoke.ErrCodeStreamError:
		return invoke.NewInvocationError(invoke.ErrCodeStreamError)
	case client.Code == invoke.ErrCodeProtocol || client.Code == invoke.ErrCodeResponseError:
		// Once dispatch has occurred, the binding-invoker boundary intentionally
		// collapses native protocol/response refinements to its generic
		// unsuccessful-interaction outcome.
		return invoke.NewInvocationError(invoke.ErrCodeExecutionFailed)
	case client.Kind == openapiclient.ErrorInput || client.Kind == openapiclient.ErrorConfiguration || client.Kind == openapiclient.ErrorOperation:
		return invoke.NewInvocationError(invoke.ErrCodeRefused)
	default:
		return invoke.NewInvocationError(invoke.ErrCodeExecutionFailed)
	}
}

func nativeSelectionInvocationError(args *invoke.BindingInvocationArgs, err error) *invoke.InvocationError {
	var client *openapiclient.ClientError
	if errors.As(err, &client) && client.Kind == openapiclient.ErrorOperation && client.Code != "SOURCE_EXCLUDED" && nativeSelectorIsDeclared(args) {
		return invoke.NewInvocationError(invoke.ErrCodeRefused)
	}
	return nativeInvocationError(err)
}

func nativeSelectorIsDeclared(args *invoke.BindingInvocationArgs) bool {
	if args == nil || args.Source.Content == nil {
		return false
	}
	content, err := openbindings.ContentToBytes(args.Source.Content)
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(content, &root) != nil {
		return false
	}
	const prefix = "#/paths/"
	if !strings.HasPrefix(args.Selector, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(args.Selector, prefix), "/")
	if len(parts) < 2 {
		return false
	}
	decode := func(value string) string {
		return strings.ReplaceAll(strings.ReplaceAll(value, "~1", "/"), "~0", "~")
	}
	paths, _ := root["paths"].(map[string]any)
	item, ok := paths[decode(parts[0])].(map[string]any)
	if !ok {
		return false
	}
	if _, present := item["$ref"]; present {
		return true
	}
	if len(parts) == 3 && parts[1] == "additionalOperations" {
		additional, _ := item["additionalOperations"].(map[string]any)
		method := decode(parts[2])
		_, present := additional[method]
		return present && nativeHTTPToken(method)
	}
	_, present := item[parts[1]]
	return present
}

func nativeHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	const admitted = "!#$%&'*+-.^_`|~"
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || strings.ContainsRune(admitted, character) {
			continue
		}
		return false
	}
	return true
}

func nativeContentEncoders(values map[string]ContentEncoder) map[string]openapiclient.ContentEncoder {
	if values == nil {
		return nil
	}
	result := make(map[string]openapiclient.ContentEncoder, len(values))
	for name, value := range values {
		result[name] = openapiclient.ContentEncoder(value)
	}
	return result
}

func nativeContentDecoders(values map[string]ContentDecoder) map[string]openapiclient.ContentDecoder {
	if values == nil {
		return nil
	}
	result := make(map[string]openapiclient.ContentDecoder, len(values))
	for name, value := range values {
		result[name] = openapiclient.ContentDecoder(value)
	}
	return result
}

func nativeCharacterEncoders(values map[string]CharacterEncoder) map[string]openapiclient.CharacterEncoder {
	if values == nil {
		return nil
	}
	result := make(map[string]openapiclient.CharacterEncoder, len(values))
	for name, value := range values {
		result[name] = openapiclient.CharacterEncoder(value)
	}
	return result
}

func nativeCharacterDecoders(values map[string]CharacterDecoder) map[string]openapiclient.CharacterDecoder {
	if values == nil {
		return nil
	}
	result := make(map[string]openapiclient.CharacterDecoder, len(values))
	for name, value := range values {
		result[name] = openapiclient.CharacterDecoder(value)
	}
	return result
}
