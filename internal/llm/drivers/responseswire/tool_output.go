// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"path/filepath"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// FunctionCallOutputItem builds the function_call_output item answering one
// tool call, for both drivers that speak the Responses API.
//
// A result with media (the view tool's screenshot, an MCP tool's image, PDF
// pages) sends the structured output form: the text as input_text, then each
// image as input_image and each other file as input_file, inside the output
// itself. The Responses API takes them there natively — codex-rs sends MCP
// image results the same way — so unlike chat completions no follow-up user
// message is needed. Both drivers used to send only the text, so the model
// read "Image file: shot.png (image/png, 241.0KB)" and never saw the image.
//
// A text-only result keeps the plain string form.
func FunctionCallOutputItem(result message.ToolResult) responses.ResponseInputItemUnionParam {
	// Built as a struct rather than via
	// ResponseInputItemParamOfFunctionCallOutput: openai-go v3.55 narrowed that
	// helper to take only the output, so it cannot set CallID. CallID is what
	// pairs this output with the function_call item, and the API rejects an
	// output that references no call.
	output := responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
		OfString: param.NewOpt(result.Content),
	}
	if len(result.BinaryParts) > 0 {
		output = responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
			OfResponseFunctionCallOutputItemArray: functionCallOutputParts(result),
		}
	}
	return responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: param.NewOpt(result.ToolCallID),
			Output: output,
		},
	}
}

func functionCallOutputParts(result message.ToolResult) responses.ResponseFunctionCallOutputItemListParam {
	parts := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(result.BinaryParts)+1)
	if result.Content != "" {
		parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{
			OfInputText: &responses.ResponseInputTextContentParam{Text: result.Content},
		})
	}
	for _, bp := range result.BinaryParts {
		if isImageMIMEType(bp.MIMEType) {
			parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{
				OfInputImage: &responses.ResponseInputImageContentParam{
					ImageURL: param.NewOpt(bp.DataURL()),
					Detail:   responses.ResponseInputImageContentDetailAuto,
				},
			})
			continue
		}
		filename := filepath.Base(bp.Path)
		if bp.Path == "" || filename == "." || filename == string(filepath.Separator) {
			filename = "file"
		}
		parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{
			OfInputFile: &responses.ResponseInputFileContentParam{
				FileData: param.NewOpt(bp.DataURL()),
				Filename: param.NewOpt(filename),
			},
		})
	}
	return parts
}

func isImageMIMEType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
