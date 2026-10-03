// CommandCode provider plugin ABI entrypoint (c-shared).
//
// Mirrors cmd/geminicli/abi.go from cpa-plugin-gemini-cli, trimmed to the
// capabilities this plugin declares: model_provider, model_router,
// executor, request/response translators. SchemaVersion tracks the host
// contract (v7.2.147 => 4).
package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int CommandCodePluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void CommandCodePluginFree(void*, size_t);
extern void CommandCodePluginShutdown(void);

static int commandcode_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}

static void commandcode_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	plug "github.com/ahoo/cpa-plugin-commandcode"
)

// pluginVersion is overridden at release build time. build.sh also injects
// the library package's descriptor version with the same value.
var pluginVersion = "0.7.1"

var abiState = struct {
	sync.RWMutex
	host   *C.cliproxy_host_api
	plugin *plug.CommandCodePlugin
}{}

type abiLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	ModelProvider         bool                         `json:"model_provider"`
	ModelRouter           bool                         `json:"model_router"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	RequestTranslator     bool                         `json:"request_translator"`
	ResponseTranslator    bool                         `json:"response_translator"`
	// ManagementAPI declares plugin-owned Management API + resource routes. The
	// host only calls management.register / management.handle when this is set.
	ManagementAPI bool `json:"management_api"`
}

// abiManagementRequest is the payload of management.handle.
type abiManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// abiManagementRegistrationResponse mirrors ManagementRegistrationResponse,
// carrying routes as JSON so the host can rehydrate their handlers.
type abiManagementRegistrationResponse struct {
	Routes    []abiManagementRoute    `json:"routes,omitempty"`
	Resources []abiManagementResource `json:"resources,omitempty"`
}

type abiManagementRoute struct {
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type abiManagementResource struct {
	Path        string `json:"path,omitempty"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type abiIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type abiExecutorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
}

type abiExecutorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type abiHostHTTPRequest struct {
	pluginapi.HTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
}

type abiHostHTTPStreamResponse struct {
	StatusCode int                         `json:"status_code"`
	Headers    http.Header                 `json:"headers,omitempty"`
	StreamID   string                      `json:"stream_id,omitempty"`
	Chunks     []pluginapi.HTTPStreamChunk `json:"chunks,omitempty"`
}

type abiHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type abiHostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type abiHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type abiHostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type abiHostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type abiEmptyResponse struct{}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	abiState.Lock()
	abiState.host = host
	abiState.Unlock()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.CommandCodePluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.CommandCodePluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.CommandCodePluginShutdown)
	return 0
}

//export CommandCodePluginCall
func CommandCodePluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleABIMethod(context.Background(), C.GoString(method), requestBytes)
	if errHandle != nil {
		writeABIResponse(response, abiErrorEnvelopeFromError("plugin_error", errHandle))
		return 1
	}
	writeABIResponse(response, raw)
	return 0
}

//export CommandCodePluginFree
func CommandCodePluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export CommandCodePluginShutdown
func CommandCodePluginShutdown() {
	abiState.Lock()
	abiState.plugin = nil
	abiState.host = nil
	abiState.Unlock()
}

func handleABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleRegister(request)
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return abiOKEnvelope(abiEmptyResponse{})
	}
	p, errPlugin := currentPlugin()
	if errPlugin != nil {
		return nil, errPlugin
	}
	switch method {
	case pluginabi.MethodExecutorIdentifier:
		return abiOKEnvelope(abiIdentifierResponse{Identifier: p.Identifier()})
	case pluginabi.MethodModelStatic:
		var req pluginapi.StaticModelRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.StaticModels(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodModelForAuth:
		var req pluginapi.AuthModelRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		req.HTTPClient = nilClient(req.HTTPClient)
		resp, errCall := p.ModelsForAuth(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodModelRoute:
		var req pluginapi.ModelRouteRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.RouteModel(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodRequestTranslate:
		var req pluginapi.RequestTransformRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.TranslateRequest(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodResponseTranslate:
		var req pluginapi.ResponseTransformRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.TranslateResponse(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodManagementRegister:
		return buildManagementRegistration(ctx)
	case pluginabi.MethodManagementHandle:
		return handleManagementRequest(ctx, request)
	case pluginabi.MethodExecutorExecute:
		var rpcReq abiExecutorRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.Execute(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodExecutorExecuteStream:
		var rpcReq abiExecutorRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.ExecuteStream(ctx, req)
		if errCall != nil {
			return nil, errCall
		}
		streamResp, errMarshal := marshalABIStreamResponse(ctx, rpcReq.StreamID, resp)
		if errMarshal != nil {
			return nil, errMarshal
		}
		return abiOKEnvelope(streamResp)
	case pluginabi.MethodExecutorCountTokens:
		var rpcReq abiExecutorRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.CountTokens(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodExecutorHTTPRequest:
		var rpcReq abiExecutorHTTPRequest
		if errDecode := json.Unmarshal(request, &rpcReq); errDecode != nil {
			return nil, errDecode
		}
		req := rpcReq.ExecutorHTTPRequest
		req.HTTPClient = abiHostHTTPClient{callbackID: rpcReq.HostCallbackID}
		resp, errCall := p.HttpRequest(ctx, req)
		return abiOKEnvelopeWithError(resp, errCall)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRegister(request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	built, p := plug.Build(req.ConfigYAML)
	if p == nil {
		return nil, fmt.Errorf("commandcode plugin registration returned invalid capabilities")
	}
	built.Metadata.Version = pluginVersion
	abiState.Lock()
	abiState.plugin = p
	abiState.Unlock()
	return abiOKEnvelope(abiRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      built.Metadata,
		Capabilities: abiCapabilities{
			ModelProvider:         built.Capabilities.ModelProvider != nil,
			ModelRouter:           built.Capabilities.ModelRouter != nil,
			Executor:              built.Capabilities.Executor != nil,
			ExecutorModelScope:    built.Capabilities.ExecutorModelScope,
			ExecutorInputFormats:  append([]string(nil), built.Capabilities.ExecutorInputFormats...),
			ExecutorOutputFormats: append([]string(nil), built.Capabilities.ExecutorOutputFormats...),
			RequestTranslator:     built.Capabilities.RequestTranslator != nil,
			ResponseTranslator:    built.Capabilities.ResponseTranslator != nil,
			ManagementAPI:         built.Capabilities.ManagementAPI != nil,
		},
	})
}

// buildManagementRegistration answers management.register.
func buildManagementRegistration(ctx context.Context) ([]byte, error) {
	p, err := currentPlugin()
	if err != nil {
		return abiErrorEnvelope("not_registered", err.Error()), nil
	}
	reg, err := p.RegisterManagement(ctx, pluginapi.ManagementRegistrationRequest{})
	if err != nil {
		return abiErrorEnvelope("management_register_failed", err.Error()), nil
	}
	out := abiManagementRegistrationResponse{}
	for _, route := range reg.Routes {
		out.Routes = append(out.Routes, abiManagementRoute{
			Method: route.Method, Path: route.Path, Menu: route.Menu, Description: route.Description,
		})
	}
	for _, res := range reg.Resources {
		out.Resources = append(out.Resources, abiManagementResource{
			Path: res.Path, Menu: res.Menu, Description: res.Description,
		})
	}
	return abiOKEnvelope(out)
}

// handleManagementRequest answers management.handle.
func handleManagementRequest(ctx context.Context, request []byte) ([]byte, error) {
	p, err := currentPlugin()
	if err != nil {
		return abiErrorEnvelope("not_registered", err.Error()), nil
	}
	var payload abiManagementRequest
	if len(request) > 0 {
		if err := json.Unmarshal(request, &payload); err != nil {
			return abiErrorEnvelope("invalid_request", "invalid management request"), nil
		}
	}
	ctx = plug.WithManagementHTTPClient(ctx, abiHostHTTPClient{callbackID: payload.HostCallbackID, cancellable: true})
	resp, err := p.HandleManagement(ctx, pluginapi.ManagementRequest{
		Method:  payload.Method,
		Path:    payload.Path,
		Headers: payload.Headers,
		Query:   payload.Query,
		Body:    payload.Body,
	})
	if err != nil {
		return abiErrorEnvelope("management_failed", err.Error()), nil
	}
	return plug.ManagementEnvelope(resp)
}

func currentPlugin() (*plug.CommandCodePlugin, error) {
	abiState.RLock()
	defer abiState.RUnlock()
	if abiState.plugin == nil {
		return nil, fmt.Errorf("commandcode plugin is not registered")
	}
	return abiState.plugin, nil
}

func nilClient(c pluginapi.HostHTTPClient) pluginapi.HostHTTPClient { return c }

type abiHostHTTPClient struct {
	callbackID  string
	cancellable bool
}

// Management calls use v8 scoped HTTP operations so their Go deadlines also
// cancel the actual host request. Executor requests keep their existing bridge.
func (c abiHostHTTPClient) operation(ctx context.Context) (string, func(), error) {
	if !c.cancellable {
		return "", func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	opened, err := callHost[struct {
		OperationID string `json:"operation_id"`
	}]("host.http.operation_open", struct {
		HostCallbackID string `json:"host_callback_id"`
	}{c.callbackID})
	if err != nil {
		return "", nil, err
	}
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		_, _ = callHost[abiEmptyResponse]("host.http.cancel", struct {
			HostCallbackID string `json:"host_callback_id"`
			OperationID    string `json:"operation_id"`
		}{c.callbackID, opened.OperationID})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	return opened.OperationID, func() { once.Do(func() { close(done) }) }, nil
}

func (c abiHostHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	operationID, stop, err := c.operation(ctx)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	defer stop()
	return callHost[pluginapi.HTTPResponse](pluginabi.MethodHostHTTPDo, abiHostHTTPRequest{
		HTTPRequest:    req,
		HostCallbackID: c.callbackID,
		OperationID:    operationID,
	})
}

func (c abiHostHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	operationID, stop, err := c.operation(ctx)
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	resp, errCall := callHost[abiHostHTTPStreamResponse](pluginabi.MethodHostHTTPDoStream, abiHostHTTPRequest{
		HTTPRequest:    req,
		HostCallbackID: c.callbackID,
		OperationID:    operationID,
	})
	if errCall != nil {
		stop()
		return pluginapi.HTTPStreamResponse{}, errCall
	}
	if resp.StreamID != "" {
		chunks := make(chan pluginapi.HTTPStreamChunk)
		go func() { defer stop(); readHostHTTPStream(ctx, resp.StreamID, chunks) }()
		return pluginapi.HTTPStreamResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Chunks: chunks}, nil
	}
	stop()
	chunks := make(chan pluginapi.HTTPStreamChunk, len(resp.Chunks))
	for _, chunk := range resp.Chunks {
		chunks <- chunk
	}
	close(chunks)
	return pluginapi.HTTPStreamResponse{StatusCode: resp.StatusCode, Headers: resp.Headers, Chunks: chunks}, nil
}

func readHostHTTPStream(ctx context.Context, streamID string, out chan<- pluginapi.HTTPStreamChunk) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			closeHostHTTPStream(streamID)
			return
		default:
		}
		resp, errRead := callHost[abiHostHTTPStreamReadResponse](pluginabi.MethodHostHTTPStreamRead, abiHostHTTPStreamReadRequest{StreamID: streamID})
		if errRead != nil {
			closeHostHTTPStream(streamID)
			select {
			case out <- pluginapi.HTTPStreamChunk{Err: errRead}:
			case <-ctx.Done():
			}
			return
		}
		if resp.Error != "" {
			select {
			case out <- pluginapi.HTTPStreamChunk{Err: fmt.Errorf("%s", resp.Error)}:
			case <-ctx.Done():
			}
			return
		}
		if len(resp.Payload) > 0 {
			select {
			case out <- pluginapi.HTTPStreamChunk{Payload: append([]byte(nil), resp.Payload...)}:
			case <-ctx.Done():
				closeHostHTTPStream(streamID)
				return
			}
		}
		if resp.Done {
			return
		}
	}
}

func closeHostHTTPStream(streamID string) {
	_, _ = callHost[abiEmptyResponse](pluginabi.MethodHostHTTPStreamClose, abiHostHTTPStreamCloseRequest{StreamID: streamID})
}

func marshalABIStreamResponse(ctx context.Context, streamID string, resp pluginapi.ExecutorStreamResponse) (abiExecutorStreamResponse, error) {
	if streamID == "" {
		chunks := make([]pluginapi.ExecutorStreamChunk, 0)
		for chunk := range resp.Chunks {
			chunks = append(chunks, chunk)
		}
		return abiExecutorStreamResponse{Headers: resp.Headers, Chunks: chunks}, nil
	}
	go pumpABIStream(ctx, streamID, resp.Chunks)
	return abiExecutorStreamResponse{Headers: resp.Headers}, nil
}

func pumpABIStream(ctx context.Context, streamID string, chunks <-chan pluginapi.ExecutorStreamChunk) {
	errorMessage := ""
	defer func() {
		_, _ = callHost[abiEmptyResponse](pluginabi.MethodHostStreamClose, abiHostStreamCloseRequest{StreamID: streamID, Error: errorMessage})
	}()
	for {
		select {
		case <-ctx.Done():
			if errCtx := ctx.Err(); errCtx != nil {
				errorMessage = errCtx.Error()
			}
			return
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				errorMessage = chunk.Err.Error()
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			_, errCall := callHost[abiEmptyResponse](pluginabi.MethodHostStreamEmit, abiHostStreamEmitRequest{
				StreamID: streamID,
				Payload:  append([]byte(nil), chunk.Payload...),
			})
			if errCall != nil {
				errorMessage = errCall.Error()
				return
			}
		}
	}
}

func callHost[T any](method string, request any) (T, error) {
	var zero T
	abiState.RLock()
	host := abiState.host
	abiState.RUnlock()
	if host == nil || host.call == nil {
		return zero, fmt.Errorf("host callback is unavailable")
	}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return zero, errMarshal
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var requestPtr *C.uint8_t
	if len(rawRequest) > 0 {
		requestPtr = (*C.uint8_t)(unsafe.Pointer(&rawRequest[0]))
	}
	var resp C.cliproxy_buffer
	code := C.commandcode_call_host(host, cMethod, requestPtr, C.size_t(len(rawRequest)), &resp)
	if resp.ptr != nil {
		defer C.commandcode_free_host_buffer(host, resp.ptr, resp.len)
	}
	if code != 0 {
		return zero, fmt.Errorf("host callback %s failed with code %d", method, int(code))
	}
	rawResp := C.GoBytes(resp.ptr, C.int(resp.len))
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(rawResp, &envelope); errDecode != nil {
		return zero, errDecode
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return zero, fmt.Errorf("%s", envelope.Error.Message)
		}
		return zero, fmt.Errorf("host callback %s failed", method)
	}
	var out T
	if len(envelope.Result) == 0 {
		return out, nil
	}
	if errDecode := json.Unmarshal(envelope.Result, &out); errDecode != nil {
		return zero, errDecode
	}
	return out, nil
}

func abiOKEnvelope(v any) ([]byte, error) {
	result, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func abiOKEnvelopeWithError(v any, err error) ([]byte, error) {
	if err != nil {
		return abiErrorEnvelopeFromError("plugin_error", err), nil
	}
	return abiOKEnvelope(v)
}

func abiErrorEnvelopeFromError(code string, err error) []byte {
	if err == nil {
		return abiErrorEnvelope(code, "")
	}
	httpStatus := 0
	if statusProvider, ok := err.(interface{ StatusCode() int }); ok && statusProvider != nil {
		httpStatus = statusProvider.StatusCode()
	}
	return abiErrorEnvelopeWithStatus(code, err.Error(), httpStatus)
}

func abiErrorEnvelope(code string, message string) []byte {
	return abiErrorEnvelopeWithStatus(code, message, 0)
}

func abiErrorEnvelopeWithStatus(code string, message string, httpStatus int) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{
		OK: false,
		Error: &pluginabi.Error{
			Code:       code,
			Message:    message,
			HTTPStatus: httpStatus,
		},
	})
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, data []byte) {
	if response == nil {
		return
	}
	if len(data) == 0 {
		response.ptr = nil
		response.len = 0
		return
	}
	ptr := C.malloc(C.size_t(len(data)))
	if ptr == nil {
		response.ptr = nil
		response.len = 0
		return
	}
	C.memcpy(ptr, unsafe.Pointer(&data[0]), C.size_t(len(data)))
	response.ptr = ptr
	response.len = C.size_t(len(data))
}

func main() {}
