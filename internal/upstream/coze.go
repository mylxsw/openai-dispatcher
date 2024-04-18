package upstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/net/proxy"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CozeUpstream struct {
	// url Destination address
	url string
	// dialer When the dialer is not empty, the dialer is used for the request
	dialer proxy.Dialer
	// key API key
	key string
}

func NewCozeUpstream(server string, key string, dialer proxy.Dialer) (*CozeUpstream, error) {
	server = strings.TrimRight(server, "/")
	if !strings.HasSuffix(server, "/open_api/v2/chat") {
		server = server + "/open_api/v2/chat"
	}

	return &CozeUpstream{
		url:    server,
		dialer: dialer,
		key:    key,
	}, nil
}

func (up *CozeUpstream) Serve(w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error)) {
	if !array.In(Endpoint(strings.TrimSuffix(r.URL.Path, "/")), []Endpoint{EndpointChatCompletion}) {
		log.F(log.M{"endpoint": r.URL.Path, "type": "coze"}).Warningf("unsupported endpoint for coze: %s", r.URL.Path)
		errorHandler(w, r, ErrUpstreamShouldRetry)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.F(log.M{"type": "coze"}).Errorf("decode request failed: %v", err)
		errorHandler(w, r, ErrUpstreamShouldRetry)
		return
	}

	client := &http.Client{}
	if up.dialer != nil {
		client.Transport = &http.Transport{
			Dial: up.dialer.Dial,
		}
	}

	if req.Stream {
		if err := up.streamHandler(client, req, w); err != nil {
			errorHandler(w, r, err)
		}
	} else {
		if err := up.nonStreamHandler(client, req, w); err != nil {
			errorHandler(w, r, err)
		}
	}
}

type CozeRequest struct {
	// BotID The ID of the bot that the API interacts with.
	// Go to the Develop page of your Coze bot. The number after the bot parameter in the page URL is the bot ID.
	// For example: https://www.coze.com/space/73428668341****/bot/73428668*****. The bot ID is73428668*****.
	BotID string `json:"bot_id"`
	// ConversationID Indicate which conversation the dialog is taking place in.
	// If there is no need to distinguish the context of the conversation (just a question and answer),
	// skip this parameter. It will be generated by the system.
	ConversationID string `json:"conversation_id,omitempty"`
	// User The user who calls the API to chat with the bot.
	User string `json:"user"`
	// Query The query sent to the bot
	Query string `json:"query"`
	// ChatHistory The chat history to pass as the context.
	// If you want to manually add the chat history in the conversation, do as follows:
	// - chat_history is a list containing the user request and conversation data returned by the API. For details, refer to the Message structure in the Response parameters section.
	// - The whole list is sorted in ascending order of time. That is, the latest message of the previous conversation appears at the end of the list.
	// - (Optional) Pass the intermediate results returned by the API back through the chat history and insert them into the chat_history in ascending order by index.
	ChatHistory []CozeMessage `json:"chat_history,omitempty"`
	// Stream Whether to stream the response to the client.
	// - false: if no value is specified or set to false, a non-streaming response is returned.
	//   "Non-streaming response" means that all responses will be returned at once after they are all ready,
	//   and the client does not need to concatenate the content.
	// - true: set to true, partial message deltas will be sent .
	//   "Streaming response" will provide real-time response of the model to the client, and the client needs to assemble
	//   the final reply based on the type of message.
	Stream bool `json:"stream"`
	// CustomVariables The customized variable in a key-value pair
	CustomVariables map[string]string `json:"custom_variables,omitempty"`
}

type CozeResponse struct {
	// ConversationID The ID of the conversation
	ConversationID string `json:"conversation_id,omitempty"`
	// Messages The completed messages returned in JSON array.
	// For details, refer to the Message structure in the Response parameters section.
	Messages []CozeMessage `json:"messages"`
	// Code The ID of the code. 0 represents a successful call.
	Code int `json:"code,omitempty"`
	// Msg The message of the request.
	Msg string `json:"msg,omitempty"`

	// for stream response

	// Event The data set returned in the event.
	// - message: the real-time message returned.
	//   data:{"event":"message","message":{"role":"assistant","type":"answer","content":"!","content_type":"text","extra_info":null},"is_finish":false,"index":0,"conversation_id":"123"}
	//
	// - done: indicates that the chat is finished.
	//   data:{"event":"done"}
	//
	// - error: indicates that an error occurs when responsing the message.
	//   data:{"event":"error", "error_information":{"code": 700007,"msg":invalid permission}}
	Event string `json:"event,omitempty"`
	// IsFinish Whether the current message is completed.
	// - false: not completed.
	// - true: completed. The current message is completed, not the whole message is sent completed when the value is true.
	IsFinish bool `json:"is_finish,omitempty"`
	// Index The identifier of the message. Each unique index corresponds to a single message.
	Index int `json:"index,omitempty"`
	// Message Incremental response messages in stream mode
	Message CozeMessage `json:"message,omitempty"`

	ErrorInformation ErrorInformation `json:"error_information,omitempty"`
}

type ErrorInformation struct {
	Code int    `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

type CozeMessage struct {
	// Role The role who returns the message
	// user: the content from user input
	// assistant: returned by bot
	Role string `json:"role"`
	// Type The type of the message when the role is assistant.
	// answer: The completed messages that the bot returns to the user.
	// function_cal：The intermediate result when deciding to use the function call during the bot conversation process.
	// tool_response：The final result from the function call.
	// follow_up：The content is returned when the auto-suggestion is enabled for the bot.
	Type string `json:"type,omitempty"`
	// Content The returned content
	Content string `json:"content"`
	// ContentType The type of the return content.
	// When the type is answer, the content will be returned in Markdown.
	ContentType string `json:"content_type,omitempty"`
}

func (up *CozeUpstream) streamHandler(client *http.Client, openaiReq openai.ChatCompletionRequest, w http.ResponseWriter) error {
	cozeReq := CozeRequest{
		BotID:  openaiReq.Model,
		User:   "apiuser",
		Stream: true,
		Query:  openaiReq.Messages[len(openaiReq.Messages)-1].Content,
		ChatHistory: array.Map(openaiReq.Messages[:len(openaiReq.Messages)-1], func(item openai.ChatCompletionMessage, _ int) CozeMessage {
			content := item.Content
			if content == "" && len(item.MultiContent) > 0 {
				for _, c := range item.MultiContent {
					if c.Type == "text" {
						content += c.Text
					}
				}
			}

			return CozeMessage{
				Role:        item.Role,
				Content:     content,
				ContentType: "text",
				Type:        ternary.If(item.Role == "assistant", "answer", ""),
			}
		}),
	}

	body, err := json.Marshal(cozeReq)
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("marshal request failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	log.Debug("coze request: ", string(body))

	req, err := http.NewRequest("POST", up.url, strings.NewReader(string(body)))
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("create request failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	req.Header.Set("Content-Type", "application/json")
	userKey := req.Header.Get("X-User-Key")
	if userKey == "" {
		req.Header.Set("Authorization", "Bearer "+up.key)
	} else {
		req.Header.Set("Authorization", "Bearer "+userKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("request failed: %v", err)
		return ErrUpstreamShouldRetry
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		log.F(log.M{"type": "coze"}).Errorf("request failed: %s", string(data))
		return ErrUpstreamShouldRetry
	}

	var outputMessage string

	defer func() {
		if err := recover(); err != nil {
			log.F(log.M{"type": "coze"}).Errorf("decode response failed: %v", err)

			if outputMessage != "" {
				finalMessage, _ := json.Marshal(openai.ChatCompletionStreamResponse{
					ID:      "final",
					Object:  "chat.completion",
					Created: time.Now().Unix(),
					Model:   openaiReq.Model,
					Choices: []openai.ChatCompletionStreamChoice{{FinishReason: openai.FinishReasonStop}},
				})
				_, _ = w.Write([]byte(fmt.Sprintf("data: %s\n\n", finalMessage)))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))

				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}()

	reader := bufio.NewReader(resp.Body)
	index := 0
	for {
		data, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}

			if outputMessage != "" {
				panic(fmt.Errorf("read response failed: %v", err))
			}

			log.F(log.M{"type": "coze"}).Errorf("read response failed: %v", err)
			return ErrUpstreamShouldRetry
		}

		//log.Debugf("coze response: %s", string(data))

		dataStr := strings.TrimSpace(string(data))
		if dataStr == "" {
			continue
		}

		if !strings.HasPrefix(dataStr, "data:") {
			continue
		}

		dataStr = strings.TrimSpace(dataStr[5:])

		var cozeResp CozeResponse
		if err := json.Unmarshal([]byte(dataStr), &cozeResp); err != nil {
			if outputMessage != "" {
				panic(fmt.Errorf("decode response failed: %v", err))
			}

			log.F(log.M{"type": "coze"}).Errorf("decode response failed: %v", err)
			return ErrUpstreamShouldRetry
		}

		if cozeResp.Event == "error" {
			if outputMessage != "" {
				panic(fmt.Errorf("chat failed: %s", cozeResp.ErrorInformation.Msg))
			}

			log.F(log.M{"type": "coze"}).Errorf("chat failed: %s", cozeResp.ErrorInformation.Msg)
			return ErrUpstreamShouldRetry
		}

		if cozeResp.Event == "message" && cozeResp.Message.Type == "answer" {
			openaiResp := openai.ChatCompletionStreamResponse{
				ID:      strconv.Itoa(index),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   openaiReq.Model,
				Choices: array.Map([]CozeMessage{cozeResp.Message}, func(item CozeMessage, i int) openai.ChatCompletionStreamChoice {
					return openai.ChatCompletionStreamChoice{
						Index: i,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Role:    item.Role,
							Content: item.Content,
						},
					}
				}),
			}

			index++

			data, _ := json.Marshal(openaiResp)
			_, _ = w.Write([]byte(fmt.Sprintf("data: %s\n\n", data)))

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return nil
}

func (up *CozeUpstream) nonStreamHandler(client *http.Client, openaiReq openai.ChatCompletionRequest, w http.ResponseWriter) error {
	cozeReq := CozeRequest{
		BotID:  openaiReq.Model,
		User:   "apiuser",
		Stream: false,
		Query:  openaiReq.Messages[len(openaiReq.Messages)-1].Content,
		ChatHistory: array.Map(openaiReq.Messages, func(item openai.ChatCompletionMessage, _ int) CozeMessage {
			content := item.Content
			if content == "" && len(item.MultiContent) > 0 {
				for _, c := range item.MultiContent {
					if c.Type == "text" {
						content += c.Text
					}
				}
			}

			return CozeMessage{
				Role:        item.Role,
				Content:     content,
				ContentType: "text",
				Type:        ternary.If(item.Role == "assistant", "answer", ""),
			}
		}),
	}

	body, err := json.Marshal(cozeReq)
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("marshal request failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	req, err := http.NewRequest("POST", up.url, strings.NewReader(string(body)))
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("create request failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	req.Header.Set("Content-Type", "application/json")
	userKey := req.Header.Get("X-User-Key")
	if userKey == "" {
		req.Header.Set("Authorization", "Bearer "+up.key)
	} else {
		req.Header.Set("Authorization", "Bearer "+userKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.F(log.M{"type": "coze"}).Errorf("request failed: %v", err)
		return ErrUpstreamShouldRetry
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.F(log.M{"type": "coze"}).Errorf("request failed: %d", resp.StatusCode)
		return ErrUpstreamShouldRetry
	}

	var cozeResp CozeResponse
	if err := json.NewDecoder(resp.Body).Decode(&cozeResp); err != nil {
		log.F(log.M{"type": "coze"}).Errorf("decode response failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	if cozeResp.Code != 0 {
		log.F(log.M{"type": "coze"}).Errorf("chat failed: %s", cozeResp.Msg)
		return ErrUpstreamShouldRetry
	}

	log.With(cozeResp).Debugf("coze non-stream response")

	openaiResp := openai.ChatCompletionResponse{
		Model: openaiReq.Model,
		Choices: array.Map(
			array.Filter(cozeResp.Messages, func(item CozeMessage, i int) bool { return item.Type == "answer" }),
			func(item CozeMessage, i int) openai.ChatCompletionChoice {
				return openai.ChatCompletionChoice{
					Index: i,
					Message: openai.ChatCompletionMessage{
						Role:    item.Role,
						Content: item.Content,
					},
				}
			},
		),
	}

	messages := append([]openai.ChatCompletionMessage{}, openaiReq.Messages...)
	messages = append(messages, array.Map(
		cozeResp.Messages,
		func(item CozeMessage, i int) openai.ChatCompletionMessage {
			return openai.ChatCompletionMessage{
				Role:    item.Role,
				Content: item.Content,
			}
		},
	)...)

	inputTokens, _ := MessageTokenCount(openaiReq.Messages, openaiReq.Model)
	totalTokens, _ := MessageTokenCount(messages, openaiReq.Model)
	if totalTokens < inputTokens {
		totalTokens = inputTokens + 200
	}

	openaiResp.Usage = openai.Usage{
		TotalTokens:      totalTokens,
		PromptTokens:     inputTokens,
		CompletionTokens: totalTokens - inputTokens,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(openaiResp); err != nil {
		w.Header().Del("Content-Type")
		log.F(log.M{"type": "coze"}).Errorf("encode response failed: %v", err)
		return ErrUpstreamShouldRetry
	}

	return nil
}
