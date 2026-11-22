// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

// Image is one image attachment to send with a turn.
type Image struct {
	Data []byte
	MIME string
	Name string
}

type agentPart struct {
	name     string
	filename string
	mime     string
	data     []byte
}

// ChatImages posts a multipart turn to the agent endpoint, which is the only
// path that accepts attachments. The text-only path stays on plain JSON.
func (c *Client) ChatImages(ctx context.Context, cred Credential, req Request, emit func(Event) error) error {
	release, err := c.lock(ctx, cred)
	if err != nil {
		return err
	}
	defer release()
	userID, signedIn, mintErr := c.resolveUser(ctx, cred)
	body, parts := agentBody(req)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	field, err := writer.CreateFormField("request")
	if err != nil {
		return err
	}
	if _, err := field.Write(body); err != nil {
		return err
	}
	for _, part := range parts {
		form, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {fmt.Sprintf("form-data; name=%q; filename=%q", part.name, part.filename)},
			"Content-Type":        {part.mime},
		})
		if err != nil {
			return err
		}
		if _, err := form.Write(part.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if os.Getenv("ALLAIIS_WEB_DEBUG") != "" {
		os.WriteFile(os.TempDir()+"/venice-agent-body.bin", buffer.Bytes(), 0o600)
	}

	for attempt := 1; ; attempt++ {
		attestation, err := c.attestation(ctx, cred, userID)
		if err != nil {
			return err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/inference/chat/agents/venice", bytes.NewReader(buffer.Bytes()))
		if err != nil {
			return err
		}
		c.headers(httpReq, cred, attestation)
		httpReq.Header.Set("Content-Type", writer.FormDataContentType())
		httpReq.Header.Set("Accept", "text/event-stream")
		if cred.DistinctID != "" {
			httpReq.Header.Set("x-venice-distinct-id", cred.DistinctID)
		}
		if token := c.sessionToken(cred, userID); token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := c.http.Do(httpReq)
		if err != nil {
			if attempt == 1 && ctx.Err() == nil {
				c.invalidateAttestation(userID)
				continue
			}
			return err
		}
		if response.StatusCode == http.StatusOK {
			delivered := false
			streamErr := decodeAgentStream(response.Body, func(event Event) error {
				delivered = true
				return emit(event)
			})
			response.Body.Close()
			if streamErr != nil && attempt == 1 && ctx.Err() == nil && !delivered {
				continue
			}
			return streamErr
		}
		status := response.StatusCode
		if status >= 500 && attempt == 1 {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			continue
		}
		if status == http.StatusUnauthorized {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if attempt == 1 && (signedIn || mintErr != nil || cred.clerkCookie("__client") != "") {
				if refreshed, _, err := c.remint(ctx, cred); err == nil {
					userID = refreshed
					continue
				}
			}
			if signedIn {
				return ErrUnauthorized
			}
			return ErrSignInRequired
		}
		if status == http.StatusForbidden && attempt == 1 {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			c.invalidateAttestation(userID)
			continue
		}
		defer response.Body.Close()
		return c.statusError(response)
	}
}

// agentBody renders the agent payload and the media parts it references.
func agentBody(req Request) ([]byte, []agentPart) {
	messages := make([]any, 0, len(req.Messages))
	parts := make([]agentPart, 0)
	for index, message := range req.Messages {
		role := message.Role
		if role == "developer" {
			role = "system"
		}
		content := make([]any, 0, 1+len(message.Images))
		content = append(content, map[string]any{
			"type": "output_text", "annotations": []any{}, "text": message.Content,
		})
		messageID := fmt.Sprintf("m%s%d", req.RequestID, index)
		for position, image := range message.Images {
			mediaID := fmt.Sprintf("%s.%d", messageID, position+1)
			attachment := map[string]any{"type": "input_image", "media_id": mediaID, "detail": "auto"}
			if !strings.HasPrefix(image.MIME, "image/") {
				attachment["type"] = "input_file"
				attachment["filename"] = imageName(messageID, position, image)
			}
			content = append(content, attachment)
			parts = append(parts, agentPart{
				name: "media." + mediaID, mime: image.MIME, data: image.Data,
				filename: imageName(messageID, position, image),
			})
		}
		entry := map[string]any{
			"role": role, "type": "message", "status": "completed", "content": content,
		}
		if len(message.Images) > 0 {
			entry["id"] = messageID
		}
		messages = append(messages, entry)
	}
	payload := map[string]any{
		"modelId": req.Model,
		"input":   map[string]any{"messages": messages},
	}
	if req.Reasoning {
		payload["reasoning"] = true
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["topP"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		payload["maxTokens"] = req.MaxTokens
	}
	body, _ := json.Marshal(payload)
	return body, parts
}

func imageName(messageID string, index int, image Image) string {
	if image.Name != "" {
		return image.Name
	}
	extension := strings.TrimPrefix(image.MIME, "image/")
	if extension == "" || extension == image.MIME {
		extension = "png"
	}
	return fmt.Sprintf("%s-%d.%s", messageID, index, extension)
}

// decodeAgentStream reads the SSE stream of the agent endpoint.
func decodeAgentStream(body io.Reader, emit func(Event) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	data := make([]string, 0, 4)
	eventName := ""
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		name := eventName
		eventName = ""
		if payload == "[DONE]" {
			return nil
		}
		var frame struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Text     string `json:"text"`
			Serving  string `json:"model_id"`
			Response struct {
				ModelID string `json:"model_id"`
				Error   struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Item struct {
				ModelID string `json:"model_id"`
			} `json:"item"`
		}
		if os.Getenv("ALLAIIS_WEB_DEBUG") != "" {
			file, _ := os.OpenFile(os.TempDir()+"/venice-agent-frames.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			file.WriteString(payload + "\n")
			file.Close()
		}
		if json.Unmarshal([]byte(payload), &frame) != nil {
			return nil
		}
		if frame.Type == "" {
			frame.Type = name
		}
		model := frame.Serving
		if model == "" {
			model = frame.Item.ModelID
		}
		if model == "" {
			model = frame.Response.ModelID
		}
		event := Event{ServingModel: model}
		switch frame.Type {
		case "response.output_text.delta":
			event.Kind, event.Content = "content", frame.Delta
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			event.Kind, event.Content = "reasoning", frame.Delta
		case "response.failed", "error":
			message := frame.Response.Error.Message
			if message == "" {
				message = "agent stream failed"
			}
			lower := strings.ToLower(message)
			switch {
			case strings.Contains(lower, "insufficientbalance"):
				return errInsufficientBalance
			case strings.Contains(lower, "prompttoolong"), strings.Contains(lower, "context length"):
				return errContextTooLong
			case strings.Contains(lower, "model"):
				return fmt.Errorf("%w: %s", errUnknownModel, message)
			}
			return errors.New(message)
		default:
			if model == "" {
				return nil
			}
			event.Kind = "meta"
		}
		return emit(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
