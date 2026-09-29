package media

import (
	"encoding/base64"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/models"
	"9router/proxy/internal/translator"
)

// antigravityProjectID pulls the Cloud project id an Antigravity connection
// carries in its provider data, checking both the legacy top-level field and
// the nested providerSpecificData form.
func antigravityProjectID(conn *models.ProviderConnection, connData *chat.ConnectionData) string {
	if conn != nil && conn.Data != "" {
		var d struct {
			ProjectID            string `json:"projectId"`
			ProviderSpecificData struct {
				ProjectID string `json:"projectId"`
			} `json:"providerSpecificData"`
		}
		if err := json.Unmarshal([]byte(conn.Data), &d); err == nil {
			if d.ProjectID != "" {
				return d.ProjectID
			}
			if d.ProviderSpecificData.ProjectID != "" {
				return d.ProviderSpecificData.ProjectID
			}
		}
	}
	if connData != nil && connData.ProviderSpecificData != nil {
		for _, key := range []string{"projectId", "project_id"} {
			if pid, ok := connData.ProviderSpecificData[key].(string); ok && pid != "" {
				return pid
			}
		}
	}
	return ""
}

// imageMIMEFor returns the content type for the requested output format.
func imageMIMEFor(format string) string {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// imageUrlFromResponse extracts the first image URL if present.
func imageUrlFromResponse(raw []byte) string {
	var openai struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &openai); err == nil && len(openai.Data) > 0 && openai.Data[0].URL != "" {
		return openai.Data[0].URL
	}
	return ""
}

// imageBase64FromResponse extracts the first inline image an OpenAI-shaped or
// raw Gemini response carries.
func imageBase64FromResponse(raw []byte) string {
	var openai struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &openai); err == nil && len(openai.Data) > 0 && openai.Data[0].B64JSON != "" {
		return openai.Data[0].B64JSON
	}

	var gemini struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &gemini); err == nil {
		for _, c := range gemini.Candidates {
			for _, p := range c.Content.Parts {
				if p.InlineData.Data != "" {
					return p.InlineData.Data
				}
			}
		}
	}
	return ""
}

// writeImageResponse returns the upstream image either as raw bytes
// (?response_format=binary, what the 9router-image skill asks for with
// --output out.png) or as OpenAI-shaped JSON with b64_json. The raw Gemini
// envelope an Antigravity image call returns is not the OpenAI shape, so it is
// normalised before being written.
func writeImageResponse(w http.ResponseWriter, r *http.Request, raw, requestBody []byte, modelInfo *chat.ModelInfo, upstreamCT string) {
	// Antigravity answers with the Gemini envelope nested one level down
	// ({"response": {"candidates": [...]}}); UnwrapAntigravityResponse inside
	// the formatter already handles that, so call it unconditionally and keep
	// the raw body only when it yields no image.
	normalised := raw
	if formatted, err := translator.FormatAntigravityImageResponse(raw, imagePromptFrom(requestBody)); err == nil &&
		imageBase64FromResponse(formatted) != "" {
		normalised = formatted
	} else {
		normalised = normalizeImageResponse(raw, imagePromptFrom(requestBody))
	}

	if strings.EqualFold(r.URL.Query().Get("response_format"), "binary") {
		var imgBytes []byte
		b64 := imageBase64FromResponse(normalised)
		if b64 != "" {
			if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
				imgBytes = decoded
			}
		} else if imgURL := imageUrlFromResponse(normalised); imgURL != "" {
			// Providers like Fal, BFL, Runway return a download URL. Fetch it so
			// the caller still receives raw image bytes as requested.
			if resp, err := http.Get(imgURL); err == nil && resp.StatusCode == http.StatusOK {
				defer resp.Body.Close()
				imgBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
			}
		}

		if len(imgBytes) > 0 {
			format := strings.ToLower(r.URL.Query().Get("output_format"))
			if format == "" {
				format = "png"
			}
			mime := imageMIMEFor(format)
			if format == "jpeg" {
				format = "jpg"
			}
			w.Header().Set(constants.HeaderContentType, mime)
			w.Header().Set("Content-Disposition", `inline; filename="image.`+format+`"`)
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			w.Write(imgBytes)
			return
		}
	}

	if upstreamCT == "" {
		upstreamCT = constants.ContentTypeJSON
	}
	w.Header().Set(constants.HeaderContentType, upstreamCT)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	w.Write(normalised)
}

// imagePromptFrom reads the prompt out of an OpenAI image request body so the
// normaliser can echo it back as revised_prompt.
func imagePromptFrom(body []byte) string {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Prompt
	}
	return ""
}

// connectionIDOf returns a connection's id or empty when there is none.
func connectionIDOf(conn *models.ProviderConnection) string {
	if conn == nil {
		return ""
	}
	return conn.ID
}

// sizeToAspectSuffix maps an OpenAI size to the "-{w}x{h}" suffix the
// Antigravity image path reads back out of the model name
// (translator.ParseImageConfig). Without it every request renders 1:1 and the
// caller's size is silently dropped.
func sizeToAspectSuffix(size string) string {
	switch strings.TrimSpace(size) {
	case "1024x1024":
		return "1x1"
	case "1792x1024":
		return "16x9"
	case "1024x1792":
		return "9x16"
	case "1536x1024":
		return "3x2"
	case "1024x1536":
		return "2x3"
	default:
		return ""
	}
}

// modelWithSizeSuffix appends the aspect suffix for the requested size, unless
// the model already carries one.
func modelWithSizeSuffix(model, size string) string {
	suffix := sizeToAspectSuffix(size)
	if suffix == "" || strings.Contains(model, "x") && strings.HasSuffix(model, suffix) {
		return model
	}
	return model + "-" + suffix
}

// sizeFromImageBody reads the OpenAI "size" field out of an image request body.
func sizeFromImageBody(body []byte) string {
	var req struct {
		Size string `json:"size"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Size
	}
	return ""
}

// bodyPrompt reads the prompt out of an image request body.
func bodyPrompt(body []byte) string {
	return imagePromptFrom(body)
}

// normalizeImageResponse inspects non-OpenAI shapes returned by diverse image
// backends and converts them to OpenAI `{created, data: [{b64_json|url}]}`.
func normalizeImageResponse(raw []byte, prompt string) []byte {
	// 1. Check if already standard OpenAI
	var openai struct {
		Data []any `json:"data"`
	}
	if err := json.Unmarshal(raw, &openai); err == nil && len(openai.Data) > 0 {
		return raw
	}

	// 2. Stability AI: {"image": "<base64>"}
	var stability struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(raw, &stability); err == nil && stability.Image != "" {
		out, _ := json.Marshal(map[string]any{
			"created": timeNowSec(),
			"data":    []map[string]string{{"b64_json": stability.Image}},
		})
		return out
	}

	// 3. SD WebUI: {"images": ["<base64>", ...]}
	var sdwebui struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &sdwebui); err == nil && len(sdwebui.Images) > 0 {
		data := make([]map[string]string, 0, len(sdwebui.Images))
		for _, img := range sdwebui.Images {
			data = append(data, map[string]string{"b64_json": img})
		}
		out, _ := json.Marshal(map[string]any{
			"created": timeNowSec(),
			"data":    data,
		})
		return out
	}

	// 4. Fal.ai / other: {"images": [{"url": "..."}]}
	var fal struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &fal); err == nil && len(fal.Images) > 0 && fal.Images[0].URL != "" {
		data := make([]map[string]string, 0, len(fal.Images))
		for _, img := range fal.Images {
			data = append(data, map[string]string{"url": img.URL})
		}
		out, _ := json.Marshal(map[string]any{
			"created": timeNowSec(),
			"data":    data,
		})
		return out
	}

	// 5. BFL: {"result": {"sample": "<url>"}}
	var bfl struct {
		Result struct {
			Sample string `json:"sample"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &bfl); err == nil && bfl.Result.Sample != "" {
		out, _ := json.Marshal(map[string]any{
			"created": timeNowSec(),
			"data":    []map[string]string{{"url": bfl.Result.Sample}},
		})
		return out
	}

	// 6. RunwayML: {"output": ["<url>", ...]}
	var runway struct {
		Output []string `json:"output"`
	}
	if err := json.Unmarshal(raw, &runway); err == nil && len(runway.Output) > 0 {
		data := make([]map[string]string, 0, len(runway.Output))
		for _, u := range runway.Output {
			data = append(data, map[string]string{"url": u})
		}
		out, _ := json.Marshal(map[string]any{
			"created": timeNowSec(),
			"data":    data,
		})
		return out
	}

	return raw
}

func timeNowSec() int64 {
	return time.Now().Unix()
}
