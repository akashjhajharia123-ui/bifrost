package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func customResponsesTool(name string) schemas.ResponsesTool {
	return schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeCustom,
		Name:        schemas.Ptr(name),
		Description: schemas.Ptr("apply a patch"),
		ResponsesToolCustom: &schemas.ResponsesToolCustom{Format: &schemas.ResponsesToolCustomFormat{
			Type:       "grammar",
			Syntax:     schemas.Ptr("lark"),
			Definition: schemas.Ptr("start: /.+/"),
		}},
	}
}

func customToolsResponsesRequest(provider schemas.ModelProvider, model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				ID:   schemas.Ptr("ctc_1"),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:                  schemas.Ptr("call_1"),
					Name:                    schemas.Ptr("apply_patch"),
					ResponsesCustomToolCall: &schemas.ResponsesCustomToolCall{Input: "*** Begin Patch \"x\""},
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_1"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{customResponsesTool("apply_patch")},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeCustom,
				Name: schemas.Ptr("apply_patch"),
			}},
		},
	}
}

func customChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeCustom,
				Custom: &schemas.ChatToolCustom{Name: "ApplyPatch", Description: schemas.Ptr("Apply a patch"), Format: &schemas.ChatToolCustomFormat{
					Type:    "grammar",
					Grammar: &schemas.ChatToolCustomGrammarFormat{Syntax: "regex", Definition: "^\\d+$"},
				}},
			}},
			ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:   schemas.ChatToolChoiceTypeCustom,
				Custom: &schemas.ChatToolChoiceCustom{Name: "ApplyPatch"},
			}},
		},
	}
}

// stubCustomToolsDatasheet gives openai/gpt-5 a true row and openai/gpt-4o a false
// row; every other pair has no row.
func stubCustomToolsDatasheet(t *testing.T) {
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.OpenAI {
			return nil
		}
		switch model {
		case "gpt-5":
			return &schemas.ModelCapabilities{SupportsCustomTools: schemas.Ptr(true)}
		case "gpt-4o":
			return &schemas.ModelCapabilities{SupportsCustomTools: schemas.Ptr(false)}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

func TestRewriteResponsesCustomTools(t *testing.T) {
	stubCustomToolsDatasheet(t)
	for _, target := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{schemas.OpenAI, "gpt-4o"},       // false row
		{schemas.OpenAI, "gpt-4.1-mini"}, // no row: unsupported by default
		{schemas.Anthropic, "claude-sonnet-4-5"},
		{schemas.Groq, "openai/gpt-oss-120b"},
	} {
		original := customToolsResponsesRequest(target.provider, target.model)
		got := RewriteResponsesCustomTools(nil, target.provider, original)
		require.NotSame(t, original, got, target.provider)

		tool := got.Params.Tools[0]
		require.Equal(t, schemas.ResponsesToolTypeFunction, tool.Type)
		require.Nil(t, tool.ResponsesToolCustom)
		require.Equal(t, "apply_patch", *tool.Name)
		require.Equal(t, "apply a patch", *tool.Description)
		require.Equal(t, []string{customToolInputParam}, tool.ResponsesToolFunction.Parameters.Required)
		input, ok := tool.ResponsesToolFunction.Parameters.Properties.Get(customToolInputParam)
		require.True(t, ok)
		description, _ := input.(*schemas.OrderedMap).Get("description")
		require.Contains(t, description, "lark")
		require.Contains(t, description, "start: /.+/")

		choice := got.Params.ToolChoice.ResponsesToolChoiceStruct
		require.Equal(t, schemas.ResponsesToolChoiceTypeFunction, choice.Type)
		require.Equal(t, "apply_patch", *choice.Name)

		require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *got.Input[0].Type)
		require.Nil(t, got.Input[0].ID, "a ctc_ id is rejected on a function_call input")
		require.JSONEq(t, `{"input":"*** Begin Patch \"x\""}`, *got.Input[0].Arguments)
		require.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *got.Input[1].Type)
		require.Equal(t, map[string]struct{}{"apply_patch": {}}, got.CustomToolNames)

		// The caller's request is untouched, since fallbacks re-run the preparation.
		require.Equal(t, schemas.ResponsesToolTypeCustom, original.Params.Tools[0].Type)
		require.Equal(t, schemas.ResponsesToolChoiceTypeCustom, original.Params.ToolChoice.ResponsesToolChoiceStruct.Type)
		require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *original.Input[0].Type)
		require.Equal(t, "ctc_1", *original.Input[0].ID)
		require.Nil(t, original.Input[0].Arguments)
		require.Equal(t, schemas.ResponsesMessageTypeCustomToolCallOutput, *original.Input[1].Type)
		require.Nil(t, original.CustomToolNames)
	}
}

func TestRewriteResponsesCustomTools_LeavesRequestAlone(t *testing.T) {
	stubCustomToolsDatasheet(t)
	supported := customToolsResponsesRequest(schemas.OpenAI, "gpt-5")
	require.Same(t, supported, RewriteResponsesCustomTools(nil, schemas.OpenAI, supported), "a true row keeps custom tools")

	plain := &schemas.BifrostResponsesRequest{Model: "claude-sonnet-4-5", Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("search")}},
	}}
	require.Same(t, plain, RewriteResponsesCustomTools(nil, schemas.Anthropic, plain))
}

// A custom tool nested in a namespace is rewritten in a copy and keyed by its namespace.
func TestRewriteResponsesCustomTools_Namespace(t *testing.T) {
	original := &schemas.BifrostResponsesRequest{Model: "m", Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
		Type: schemas.ResponsesToolTypeNamespace,
		Name: schemas.Ptr("shell"),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			customResponsesTool("run"),
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("wait"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		}},
	}}}}
	got := RewriteResponsesCustomTools(nil, schemas.Bedrock, original)
	members := got.Params.Tools[0].ResponsesToolNamespace.Tools
	require.Equal(t, schemas.ResponsesToolTypeFunction, members[0].Type)
	require.Equal(t, schemas.ResponsesToolTypeFunction, members[1].Type)
	require.Equal(t, map[string]struct{}{"shell\x00run": {}}, got.CustomToolNames)
	require.Equal(t, schemas.ResponsesToolTypeCustom, original.Params.Tools[0].ResponsesToolNamespace.Tools[0].Type)
}

func TestResponsesCustomToolChoiceAsFunction_AllowedTools(t *testing.T) {
	original := &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeAllowedTools,
		Mode: schemas.Ptr("required"),
		Tools: []schemas.ResponsesToolChoiceAllowedToolDef{
			{Type: "custom", Name: schemas.Ptr("apply_patch")},
			{Type: "function", Name: schemas.Ptr("search")},
		},
	}}
	got := responsesCustomToolChoiceAsFunction(original)
	require.Equal(t, "function", got.ResponsesToolChoiceStruct.Tools[0].Type)
	require.Equal(t, "function", got.ResponsesToolChoiceStruct.Tools[1].Type)
	require.Equal(t, "custom", original.ResponsesToolChoiceStruct.Tools[0].Type)
}

func TestResponsesAdditionalToolsNeedHoist(t *testing.T) {
	stubCustomToolsDatasheet(t)
	item := func(tools string) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{Model: "gpt-4o", Input: []schemas.ResponsesMessage{{
			Type:            schemas.Ptr(schemas.ResponsesMessageTypeAdditionalTools),
			AdditionalTools: []byte(tools),
		}}}
	}
	custom := `[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]`
	require.True(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(custom)))
	require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(`[{"type":"function","name":"wait"}]`)))
	supported := item(custom)
	supported.Model = "gpt-5"
	require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, supported))
}

func TestRewriteChatCustomTools(t *testing.T) {
	stubCustomToolsDatasheet(t)
	original := customChatRequest(schemas.Anthropic, "claude-sonnet-4-5")
	got := RewriteChatCustomTools(nil, schemas.Anthropic, original)

	tool := got.Params.Tools[0]
	require.Equal(t, schemas.ChatToolTypeFunction, tool.Type)
	require.Nil(t, tool.Custom)
	require.Equal(t, "ApplyPatch", tool.Function.Name)
	require.Equal(t, "Apply a patch", *tool.Function.Description)
	input, ok := tool.Function.Parameters.Properties.Get(customToolInputParam)
	require.True(t, ok)
	description, _ := input.(*schemas.OrderedMap).Get("description")
	require.Contains(t, description, "^\\d+$")
	body, err := schemas.MarshalSorted(tool)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"custom"`)

	choice := got.Params.ToolChoice.ChatToolChoiceStruct
	require.Equal(t, schemas.ChatToolChoiceTypeFunction, choice.Type)
	require.Equal(t, "ApplyPatch", choice.Function.Name)

	require.Equal(t, schemas.ChatToolTypeCustom, original.Params.Tools[0].Type)
	require.Equal(t, schemas.ChatToolChoiceTypeCustom, original.Params.ToolChoice.ChatToolChoiceStruct.Type)

	supported := customChatRequest(schemas.OpenAI, "gpt-5")
	require.Same(t, supported, RewriteChatCustomTools(nil, schemas.OpenAI, supported))
}

func TestRestoreResponsesCustomToolCalls(t *testing.T) {
	names := map[string]struct{}{"apply_patch": {}}
	resp := &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr("call_1"),
				Name:      schemas.Ptr("apply_patch"),
				Arguments: schemas.Ptr(`{"input":"line one\nline two"}`),
			},
		},
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(`not json`)},
		},
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("search"), Arguments: schemas.Ptr(`{"q":"x"}`)},
		},
	}}
	RestoreResponsesCustomToolCalls(names, resp)
	output := resp.Output

	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *output[0].Type)
	require.Nil(t, output[0].Arguments)
	require.Equal(t, "call_1", *output[0].CallID)
	require.Equal(t, "line one\nline two", output[0].ResponsesCustomToolCall.Input)

	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *output[1].Type)
	require.Equal(t, "not json", output[1].ResponsesCustomToolCall.Input)

	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *output[2].Type)
	require.Equal(t, `{"q":"x"}`, *output[2].Arguments)
}

// Compaction replays the conversation without tools: custom calls are rewritten in a
// copy, items without a type are skipped, and a kept function call is restored by the
// namespace and name of the custom call it came from.
func TestRewriteCompactionCustomCalls(t *testing.T) {
	stubCustomToolsDatasheet(t)
	history := customToolsResponsesRequest(schemas.XAI, "grok-4").Input
	history[0].Namespace = schemas.Ptr("tools")
	untyped := schemas.ResponsesMessage{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}
	req := &schemas.BifrostCompactionRequest{Provider: schemas.XAI, Model: "grok-4", Input: append([]schemas.ResponsesMessage{untyped}, history...)}

	got, names := RewriteCompactionCustomCalls(nil, schemas.XAI, req)
	require.NotSame(t, req, got)
	require.Equal(t, map[string]struct{}{"tools\x00apply_patch": {}}, names)
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *got.Input[1].Type)
	require.Nil(t, got.Input[1].ID, "a ctc_ id is rejected on a function_call input")
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *got.Input[2].Type)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *req.Input[1].Type, "the caller's request is untouched")

	resp := &schemas.BifrostCompactionResponse{Output: []schemas.ResponsesMessage{got.Input[1], {
		Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr("{}")},
	}}}
	RestoreCompactionCustomToolCalls(names, resp)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *resp.Output[0].Type)
	require.Equal(t, `*** Begin Patch "x"`, resp.Output[0].ResponsesCustomToolCall.Input)
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *resp.Output[1].Type, "a same-named call outside the namespace stays a function call")
}

// Nothing to rewrite, or a model with a true row, returns the request itself.
func TestRewriteCompactionCustomCalls_LeavesRequestAlone(t *testing.T) {
	stubCustomToolsDatasheet(t)
	plain := &schemas.BifrostCompactionRequest{Provider: schemas.XAI, Model: "grok-4", Input: []schemas.ResponsesMessage{{Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}}}
	got, names := RewriteCompactionCustomCalls(nil, schemas.XAI, plain)
	require.Same(t, plain, got)
	require.Nil(t, names)

	supported := &schemas.BifrostCompactionRequest{Provider: schemas.OpenAI, Model: "gpt-5", Input: customToolsResponsesRequest(schemas.OpenAI, "gpt-5").Input}
	got, names = RewriteCompactionCustomCalls(nil, schemas.OpenAI, supported)
	require.Same(t, supported, got)
	require.Nil(t, names)

	got, names = RewriteCompactionCustomCalls(nil, schemas.OpenAI, nil)
	require.Nil(t, got)
	require.Nil(t, names)
	RestoreCompactionCustomToolCalls(map[string]struct{}{"x": {}}, nil)
}

// A custom tool and a function tool may share a name in different namespaces;
// only the call to the rewritten custom tool is restored, and it keeps its namespace.
func TestRestoreCustomToolCall_MatchesNamespace(t *testing.T) {
	names := map[string]struct{}{}
	collectCustomToolNames([]schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("shell"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("run")},
		}}},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("jobs"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("run"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		}}},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("functions"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("exec")},
		}}},
	}, "", names)

	call := func(name, namespace string) *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Name:      schemas.Ptr(name),
				Namespace: schemas.Ptr(namespace),
				Arguments: schemas.Ptr(`{"input":"ls"}`),
			},
		}
	}
	custom, function, defaultNamespace := call("run", "shell"), call("run", "jobs"), call("exec", "functions")
	require.True(t, restoreCustomToolCall(custom, names))
	require.Equal(t, "ls", custom.ResponsesCustomToolCall.Input)
	require.Equal(t, "shell", *custom.Namespace)
	require.False(t, restoreCustomToolCall(function, names))
	require.Equal(t, `{"input":"ls"}`, *function.Arguments)
	require.True(t, restoreCustomToolCall(defaultNamespace, names), "functions is the default namespace")
}

// runStream feeds events through the restore wrapper and returns what reaches the post hooks.
func runStream(t *testing.T, names map[string]struct{}, events []*schemas.BifrostResponsesStreamResponse) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	var sent []*schemas.BifrostResponsesStreamResponse
	runner := WrapCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		sent = append(sent, result.ResponsesStreamResponse)
		return result, err
	}, names)
	for _, event := range events {
		_, err := runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
		if err != nil {
			require.True(t, isStreamControlSkip(err), "only skip controls are expected")
		}
	}
	return sent
}

func TestWrapCustomToolRestorePostHookRunner(t *testing.T) {
	inner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}
	require.NotNil(t, WrapCustomToolRestorePostHookRunner(inner, nil))

	itemID := schemas.Ptr("fc_1")
	call := func(eventType schemas.ResponsesStreamResponseType, args string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: eventType, Item: &schemas.ResponsesMessage{
			ID:                   itemID,
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(args)},
		}}
	}
	delta := func(id *string, d string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, ItemID: id, Delta: schemas.Ptr(d)}
	}
	events := []*schemas.BifrostResponsesStreamResponse{
		call(schemas.ResponsesStreamResponseTypeOutputItemAdded, ""),
		delta(itemID, `{"inp`),
		delta(itemID, `ut": "ab`),
		delta(schemas.Ptr("fc_other"), `{}`),
		delta(itemID, `c\n`),
		delta(itemID, `d"}`),
		{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, ItemID: itemID, Arguments: schemas.Ptr(`{"input": "abc\nd"}`)},
		call(schemas.ResponsesStreamResponseTypeOutputItemDone, `{"input": "abc\nd"}`),
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{*call("", `{"input": "abc\nd"}`).Item}}},
	}
	sent := runStream(t, map[string]struct{}{"apply_patch": {}}, events)

	var types []schemas.ResponsesStreamResponseType
	for _, event := range sent {
		types = append(types, event.Type)
	}
	require.Equal(t, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, // an unrelated call passes through
		schemas.ResponsesStreamResponseTypeCustomToolCallInputDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	}, types, "argument deltas of a restored call are dropped")
	require.Equal(t, "fc_other", *sent[1].ItemID)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *sent[0].Item.Type)
	require.Equal(t, "abc\nd", *sent[2].Input)
	require.Nil(t, sent[2].Arguments)
	require.Equal(t, "abc\nd", sent[3].Item.ResponsesCustomToolCall.Input)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *sent[4].Response.Output[0].Type)
}

// BenchmarkRewriteCustomTools_NoCustomTools measures the common case on both routes:
// requests that declare no custom tool must cost a scan and nothing else.
func BenchmarkRewriteCustomTools_NoCustomTools(b *testing.B) {
	responses := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("shell"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		{Type: schemas.ResponsesToolTypeWebSearch},
	}}}
	chat := &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{
		{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "shell"}},
	}}}
	b.ReportAllocs()
	for b.Loop() {
		_ = ResponsesAdditionalToolsNeedHoist(nil, schemas.Anthropic, responses)
		_ = RewriteResponsesCustomTools(nil, schemas.Anthropic, responses)
		_ = RewriteChatCustomTools(nil, schemas.Anthropic, chat)
	}
}

// BenchmarkRewriteResponsesCustomTools_Rewrite measures a request whose custom tool is rewritten.
func BenchmarkRewriteResponsesCustomTools_Rewrite(b *testing.B) {
	request := customToolsResponsesRequest(schemas.Anthropic, "claude-sonnet-4-5")
	b.ReportAllocs()
	for b.Loop() {
		_ = RewriteResponsesCustomTools(nil, schemas.Anthropic, request)
	}
}

// BenchmarkCustomToolStreamRestore measures dropping one argument delta of a restored custom call.
func BenchmarkCustomToolStreamRestore(b *testing.B) {
	names := map[string]struct{}{"apply_patch": {}}
	runner := WrapCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}, names)
	itemID := schemas.Ptr("fc_1")
	added := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{
		ID: itemID, Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch")},
	}}
	_, _ = runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: added}, nil)
	b.ReportAllocs()
	for b.Loop() {
		event := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, ItemID: itemID, Delta: schemas.Ptr("+ line of patch\\n")}
		_, _ = runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
	}
}
