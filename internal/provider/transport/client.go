package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/must"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"github.com/sashabaranov/go-openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	// url Destination address
	url    *url.URL
	server string
	key    string
	// dialer When the dialer is not empty, the dialer is used for the request
	dialer proxy.Dialer
	// director Request edit
	director func(req *http.Request)
	replace  func(model string) string
}

func New(server string, key string, dialer proxy.Dialer, replace func(model string) string) (*Client, error) {
	target, err := url.Parse(server)
	if err != nil {
		return nil, err
	}

	return &Client{
		url:    target,
		server: server,
		key:    key,
		dialer: dialer,
		director: func(r *http.Request) {
			// When the request header X-User-Key is specified in the request, the user's own key is used
			userKey := r.Header.Get("X-User-Key")
			if userKey != "" {
				r.Header.Set("Authorization", "Bearer "+userKey)
			} else {
				r.Header.Set("Authorization", "Bearer "+key)
			}
		},
		replace: replace,
	}, nil
}

func (target *Client) readRequestBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	return body, nil
}

func (target *Client) replaceRequestBody(r *http.Request, newBody []byte) {
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewBuffer(newBody))
	r.ContentLength = int64(len(newBody))
}

func (target *Client) Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error)) {

	if target.replace != nil && base.EndpointHasModel(r.URL.Path) && !array.In(r.Method, []string{"GET", "OPTIONS", "HEAD"}) {
		body, err := target.readRequestBody(r)
		if err != nil {
			errorHandler(w, r, err)
			return
		}

		newModel := target.replace(gjson.Get(string(body), "model").String())
		newBody, _ := sjson.Set(string(body), "model", newModel)
		if strings.HasPrefix(newModel, "o1-") && gjson.Get(string(body), "stream").Bool() {
			// Temporary solution, as the current o1 series model does not support streaming.
			// If the model is o1-*, the stream field is forcibly set to false
			var reqBody openai.ChatCompletionRequest
			if err := json.Unmarshal([]byte(newBody), &reqBody); err != nil {
				errorHandler(w, r, err)
				return
			}

			reqBody.Stream = false
			includeUsage := reqBody.StreamOptions != nil && reqBody.StreamOptions.IncludeUsage
			reqBody.StreamOptions = nil

			// replace system role with user role
			reqBody.Messages = array.Map(reqBody.Messages, func(item openai.ChatCompletionMessage, index int) openai.ChatCompletionMessage {
				if item.Role == "system" {
					item.Role = "user"
				}

				return item
			})

			// send a post request to the server
			req, err := http.NewRequestWithContext(ctx, "POST", must.Must(url.JoinPath(target.server, "/v1/chat/completions")), strings.NewReader(string(must.Must(json.Marshal(reqBody)))))
			if err != nil {
				errorHandler(w, r, err)
				return
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+target.key)

			client := &http.Client{}
			if target.dialer != nil {
				client.Transport = &http.Transport{
					Dial: target.dialer.Dial,
				}
			}

			resp, err := client.Do(req)
			if err != nil {
				log.F(log.M{"type": "openai"}).Errorf("request failed: %v", err)
				errorHandler(w, r, base.ErrUpstreamShouldRetry)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
				data, _ := io.ReadAll(resp.Body)
				log.F(log.M{"type": "openai"}).Errorf("request failed: %s", string(data))
				errorHandler(w, r, base.ErrUpstreamShouldRetry)
				return
			}

			data, _ := io.ReadAll(resp.Body)
			var openaiResp openai.ChatCompletionResponse
			if err := json.Unmarshal(data, &openaiResp); err != nil {
				log.F(log.M{"type": "openai"}).Errorf("unmarshal response failed: %v", err)
				errorHandler(w, r, base.ErrUpstreamShouldRetry)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			streamResp := openai.ChatCompletionStreamResponse{
				ID:      openaiResp.ID,
				Object:  "chat.completion.chunk",
				Created: openaiResp.Created,
				Model:   openaiResp.Model,
				Choices: array.Map(openaiResp.Choices, func(item openai.ChatCompletionChoice, index int) openai.ChatCompletionStreamChoice {
					return openai.ChatCompletionStreamChoice{
						Index: item.Index,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Content:      item.Message.Content,
							Role:         item.Message.Role,
							FunctionCall: item.Message.FunctionCall,
							ToolCalls:    item.Message.ToolCalls,
						},
						FinishReason: item.FinishReason,
					}
				}),
				Usage: nil,
			}

			streamRespData, _ := json.Marshal(streamResp)
			_, _ = w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(streamRespData))))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			if includeUsage {
				// If includeUsage, an additional chunk will be streamed before the data: [DONE] message.
				// The usage field on this chunk shows the token usage statistics for the entire request,
				// and the choices field will always be an empty array.
				// All other chunks will also include a usage field, but with a null value.
				usage, _ := json.Marshal(openai.ChatCompletionStreamResponse{
					ID:      openaiResp.ID + "-usage",
					Object:  "chat.completion.chunk",
					Created: openaiResp.Created,
					Model:   openaiResp.Model,
					Choices: []openai.ChatCompletionStreamChoice{},
					Usage:   &openaiResp.Usage,
				})

				_, _ = w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(usage))))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}

			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			return
		}

		body = []byte(newBody)
		target.replaceRequestBody(r, body)
	}

	// Proxy forwarding
	revProxy := httputil.NewSingleHostReverseProxy(target.url)
	if target.dialer != nil {
		revProxy.Transport = &http.Transport{
			Dial:                  target.dialer.Dial,
			ResponseHeaderTimeout: 10 * time.Second,
		}
	} else {
		revProxy.Transport = &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		}
	}

	originalDirector := revProxy.Director

	startTime := time.Now()
	revProxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.url.Host

		if target.director != nil {
			target.director(req)
		}
	}
	revProxy.ModifyResponse = func(resp *http.Response) error {
		if log.DebugEnabled() {
			log.Debugf("request: %s %s [%d] %v", resp.Request.Method, resp.Request.URL.String(), resp.StatusCode, time.Since(startTime))
		}

		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w | %w", parseErrorMessage(resp), base.ErrUpstreamShouldRetry)
		}

		switch resp.StatusCode {
		case 400:
			// 400 Error Responding to a content filtering issue with Azure
			parsed := parseErrorMessage(resp)
			if strings.Contains(parsed.Error(), "content_filter") {
				return fmt.Errorf("%w | %w", base.ResponseError{Err: parsed, Resp: resp}, base.ErrUpstreamShouldRetry)
			}
		case 403:
			// 403 mistake
			return fmt.Errorf("%w | %w", base.ResponseError{Err: parseErrorMessage(resp), Resp: resp}, base.ErrUpstreamShouldRetry)
		case 404:
			// 404 Error, addressing the lack of a specific model for Azure
			parsed := parseErrorMessage(resp)
			if strings.Contains(parsed.Error(), "DeploymentNotFound") {
				return fmt.Errorf("%w | %w", base.ResponseError{Err: parsed, Resp: resp}, base.ErrUpstreamShouldRetry)
			}
		case 401, 429:
			// Authentication and flow control errors
			return fmt.Errorf("%w | %w", base.ResponseError{Err: parseErrorMessage(resp), Resp: resp}, base.ErrUpstreamShouldRetry)
		}

		return nil
	}

	revProxy.ErrorHandler = errorHandler

	revProxy.ServeHTTP(w, r)
}

func parseErrorMessage(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewBuffer(data))

	dataStr := string(data)
	if dataStr == "" {
		return fmt.Errorf("[%d] %s", resp.StatusCode, resp.Status)
	}

	var message string
	errRes := gjson.Get(dataStr, "error")
	if errRes.Exists() {
		if errRes.IsObject() {
			message = errRes.Get("message").String()
		} else {
			message = errRes.String()
		}
	} else {
		message = gjson.Get(dataStr, "message").String()
	}

	code := gjson.Get(dataStr, "code").String()
	if code == "" {
		code = gjson.Get(dataStr, "error.code").String()
	}
	if code != "" {
		message = fmt.Sprintf("[%s] %s", code, message)
	}

	return fmt.Errorf("[%d] %s", resp.StatusCode, message)
}
