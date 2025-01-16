package base

import (
	"context"
	"errors"
	"github.com/mylxsw/go-utils/array"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"strings"
)

var (
	ErrUpstreamShouldRetry = errors.New("upstream failed")
)

type Endpoint string

const (
	EndpointChatCompletion  Endpoint = "/v1/chat/completions"
	EndpointCompletion      Endpoint = "/v1/completions"
	EndpointImageGeneration Endpoint = "/v1/images/generations"
	EndpointImageEdit       Endpoint = "/v1/images/edits"
	EndpointImageVariation  Endpoint = "/v1/images/variations"
	EndpointAudioSpeech     Endpoint = "/v1/audio/speech"
	EndpointAudioTranscript Endpoint = "/v1/audio/transcriptions"
	EndpointAudioTranslate  Endpoint = "/v1/audio/translations"
	EndpointModeration      Endpoint = "/v1/moderations"
	EndpointEmbedding       Endpoint = "/v1/embeddings"
)

func EndpointNeedModeration(path string) bool {
	return array.In(Endpoint(strings.TrimSuffix(path, "/")), []Endpoint{
		EndpointChatCompletion,
		EndpointCompletion,
	})
}

func EndpointHasModel(path string) bool {
	return array.In(Endpoint(strings.TrimSuffix(path, "/")), []Endpoint{
		EndpointChatCompletion,
		EndpointCompletion,
		EndpointImageGeneration,
		EndpointImageEdit,
		EndpointImageVariation,
		EndpointAudioSpeech,
		EndpointAudioTranscript,
		EndpointAudioTranslate,
		EndpointModeration,
		EndpointEmbedding,
	})
}

type Handler interface {
	Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error))
}

type Provider interface {
	Completion(ctx context.Context, openaiReq openai.ChatCompletionRequest, w http.ResponseWriter) error
	CompletionStream(ctx context.Context, openaiReq openai.ChatCompletionRequest, w http.ResponseWriter) error
}

type ResponseError struct {
	Err  error
	Resp *http.Response
}

func (r ResponseError) Error() string {
	return r.Err.Error()
}

type ChannelType string

const (
	ChannelTypeOpenAI    ChannelType = "openai"
	ChannelTypeAzure     ChannelType = "azure"
	ChannelTypeCoze      ChannelType = "coze"
	ChannelTypeAnthropic ChannelType = "anthropic"
)
