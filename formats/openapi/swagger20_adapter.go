package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// runSwagger20 is the edition-specific adapter lane while the 2.0 token
// remains registered but unwarranted. It owns the OpenBindings envelope,
// qualified-key, and configuration vocabulary; the client receives only
// location-separated OpenAPI-native values.
func (e *Runtime) runSwagger20(ctx context.Context, args *invoke.BindingInvocationArgs, inv *invoke.InvocationImpl[any, any]) error {
	if args == nil {
		return &invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError}
	}
	var content []byte
	if args.Source.Content != nil {
		var err error
		content, err = openbindings.ContentToBytes(args.Source.Content)
		if err != nil {
			return &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
		}
	}
	configuration, configErr := swagger20Configuration(args.Context)
	credentials := swagger20Credentials(args.Context)
	prepared, err := e.engine.PrepareSwagger20(ctx, openapiclient.Swagger20PrepareOptions{
		Source: openapiclient.Swagger20Source{
			Location: args.Source.Location,
			Content:  content,
		},
		Ref:                    args.Selector,
		Context:                args.Context,
		HTTPClient:             e.client,
		Server:                 configuration.server,
		ServerSchemeIndex:      configuration.serverSchemeIndex,
		SecurityAlternative:    configuration.securityAlternative,
		SecurityCredentials:    credentials,
		RequestMedia:           configuration.requestMedia,
		PropertyMedia:          configuration.propertyMedia,
		ParameterConverter:     e.parameterConvert,
		EmptyValueForm:         configuration.emptyValueForm,
		RequestContentCodings:  e.requestCodings,
		ResponseContentCodings: e.responseCodings,
		MaxDeliveryUnitBytes:   args.MaxDeliveryUnitBytes,
	})
	if err != nil {
		return bridgeExecutionError(err)
	}
	if configErr != nil {
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	parameters, err := prepared.Parameters()
	if err != nil {
		return bridgeExecutionError(err)
	}
	bridgeCtx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()
	execution, err := prepared.Start(bridgeCtx)
	if err != nil {
		return bridgeExecutionError(err)
	}
	if execution.InputRequested() {
		value, readErr := inv.ReadInput(bridgeCtx)
		switch {
		case errors.Is(readErr, io.EOF):
			if err := execution.FinishInput(); err != nil {
				return bridgeExecutionError(err)
			}
		case readErr != nil:
			execution.Cancel()
			return readErr
		default:
			engineInput, inputErr := swagger20InputForCallerEnvelope(value, parameters)
			if inputErr != nil {
				execution.Cancel()
				return invoke.NewInvocationError(openapiclient.CodeRefused)
			}
			if err := execution.Send(bridgeCtx, engineInput); err != nil {
				return bridgeExecutionError(err)
			}
			if err := execution.FinishInput(); err != nil {
				return bridgeExecutionError(err)
			}
		}
	}
	_ = inv.CloseInput()

	_, _ = execution.Response(bridgeCtx)
	for event := range execution.Events() {
		if err := inv.EmitOutput(event.Value); err != nil {
			execution.Cancel()
			return nil
		}
	}
	if err := execution.Wait(); err != nil {
		if bridgeCtx.Err() != nil {
			return nil
		}
		return bridgeExecutionError(err)
	}
	inv.CloseOutput()
	return nil
}

type swagger20RuntimeConfiguration struct {
	server              string
	serverSchemeIndex   *int
	securityAlternative *int
	requestMedia        string
	propertyMedia       map[string]string
	emptyValueForm      openapiclient.Swagger20EmptyValueForm
}

func swagger20Configuration(bindCtx map[string]any) (swagger20RuntimeConfiguration, error) {
	configuration := invoke.ContextConfiguration(bindCtx)
	var result swagger20RuntimeConfiguration
	if raw, present := configuration["server"]; present {
		switch typed := raw.(type) {
		case string:
			result.server = typed
		case map[string]any:
			if len(typed) != 1 {
				return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.server object must contain only baseUrl or index")
			}
			if rawIndex, ok := typed["index"]; ok {
				index, valid := swagger20ConfigIndex(rawIndex)
				if !valid || index < 0 {
					return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.server index must be a nonnegative integer")
				}
				result.serverSchemeIndex = &index
			} else {
				result.server, _ = typed["baseUrl"].(string)
				if result.server == "" {
					return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.server baseUrl must be a nonempty complete URL")
				}
			}
		default:
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.server must be a complete URL string, baseUrl object, or index object")
		}
		if result.server == "" && result.serverSchemeIndex == nil {
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.server must contain a nonempty complete URL")
		}
	}
	if raw, present := configuration["security"]; present {
		object, ok := raw.(map[string]any)
		if !ok || len(object) != 1 {
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.security must be an object containing only index")
		}
		index, valid := swagger20ConfigIndex(object["index"])
		if !valid || index < 0 {
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.security index must be a nonnegative integer")
		}
		result.securityAlternative = &index
	}
	if raw, present := configuration["requestMedia"]; present {
		text, ok := raw.(string)
		if !ok || text == "" {
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.requestMedia must be a nonempty string")
		}
		result.requestMedia = text
	}
	if raw, present := configuration["propertyMedia"]; present {
		values := map[string]any{}
		switch typed := raw.(type) {
		case map[string]any:
			values = typed
		case map[string]string:
			for name, value := range typed {
				values[name] = value
			}
		default:
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.propertyMedia must be an object")
		}
		result.propertyMedia = make(map[string]string, len(values))
		for name, rawValue := range values {
			text, ok := rawValue.(string)
			if name == "" || !ok || text == "" {
				return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.propertyMedia values must map nonempty names to nonempty strings")
			}
			result.propertyMedia[name] = text
		}
	}
	if raw, present := configuration["emptyValueForm"]; present {
		text, ok := raw.(string)
		if !ok || (text != string(openapiclient.Swagger20EmptyValueNameOnly) && text != string(openapiclient.Swagger20EmptyValueEmpty)) {
			return swagger20RuntimeConfiguration{}, fmt.Errorf("configuration.emptyValueForm must be name-only or empty")
		}
		result.emptyValueForm = openapiclient.Swagger20EmptyValueForm(text)
	}
	return result, nil
}

func swagger20ConfigIndex(raw any) (int, bool) {
	if number, ok := raw.(json.Number); ok {
		integer, err := number.Int64()
		return int(integer), err == nil && int64(int(integer)) == integer
	}
	return configIndex(raw)
}

func swagger20Credentials(bindCtx map[string]any) openapiclient.Swagger20SecurityCredentials {
	result := openapiclient.Swagger20SecurityCredentials{
		Basic: map[string]openapiclient.Swagger20BasicCredential{}, APIKeys: map[string]string{}, OAuth2: map[string]openapiclient.Swagger20OAuth2Credential{},
	}
	add := func(values map[string]any) {
		for name, raw := range values {
			switch credential := raw.(type) {
			case string:
				result.APIKeys[name] = credential
			case map[string]any:
				userID, userPresent := credential["userId"].(string)
				if !userPresent {
					userID, userPresent = credential["username"].(string)
				}
				password, passwordPresent := credential["password"].(string)
				if userPresent && passwordPresent {
					result.Basic[name] = openapiclient.Swagger20BasicCredential{UserID: userID, Password: password}
				}
				accessToken, tokenPresent := credential["accessToken"].(string)
				if tokenPresent {
					scopes, valid := swagger20CredentialScopes(credential["scopes"])
					if valid {
						result.OAuth2[name] = openapiclient.Swagger20OAuth2Credential{AccessToken: accessToken, Scopes: scopes}
					}
				}
			}
		}
	}
	if values, ok := bindCtx["credentials"].(map[string]any); ok {
		add(values)
	}
	if values, ok := bindCtx["apiKeys"].(map[string]any); ok {
		add(values)
	}
	return result
}

func swagger20CredentialScopes(raw any) ([]string, bool) {
	if raw == nil {
		return nil, true
	}
	values, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return append([]string(nil), typed...), true
		}
		return nil, false
	}
	result := make([]string, len(values))
	for index, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return nil, false
		}
		result[index] = value
	}
	return result, true
}

func swagger20InputForCallerEnvelope(input any, parameters []openapiclient.Swagger20ParameterInfo) (openapiclient.Swagger20Input, error) {
	envelope, err := parseCallerEnvelope(input)
	if err != nil {
		return openapiclient.Swagger20Input{}, err
	}
	locations := map[string]openapiclient.Swagger20ParameterLocation{}
	qualified := false
	for _, parameter := range parameters {
		if parameter.In == openapiclient.Swagger20ParameterBody {
			continue
		}
		if previous, present := locations[parameter.Name]; present && previous != parameter.In {
			qualified = true
		}
		locations[parameter.Name] = parameter.In
	}
	result := openapiclient.Swagger20Input{
		Parameters: openapiclient.Swagger20Parameters{
			Path: map[string]any{}, Query: map[string]any{}, Header: map[string]any{}, FormData: map[string]any{},
		},
		Body: envelope.body, BodyPresent: envelope.bodyPresent,
	}
	byCallerKey := map[string]openapiclient.Swagger20ParameterInfo{}
	for _, parameter := range parameters {
		if parameter.In == openapiclient.Swagger20ParameterBody {
			continue
		}
		key := parameter.Name
		if qualified {
			key = string(parameter.In) + "/" + escapeJSONPointerSegment(parameter.Name)
		}
		byCallerKey[key] = parameter
	}
	for key, value := range envelope.parameters {
		parameter, present := byCallerKey[key]
		if !present {
			return openapiclient.Swagger20Input{}, fmt.Errorf("caller envelope contains unknown parameter key %q", key)
		}
		switch parameter.In {
		case openapiclient.Swagger20ParameterPath:
			result.Parameters.Path[parameter.Name] = value
		case openapiclient.Swagger20ParameterQuery:
			result.Parameters.Query[parameter.Name] = value
		case openapiclient.Swagger20ParameterHeader:
			result.Parameters.Header[parameter.Name] = value
		case openapiclient.Swagger20ParameterFormData:
			result.Parameters.FormData[parameter.Name] = value
		}
	}
	return result, nil
}
