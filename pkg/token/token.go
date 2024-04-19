package token

import (
	"fmt"
	"github.com/mylxsw/go-utils/array"
	"github.com/pkoukk/tiktoken-go"
	"github.com/sashabaranov/go-openai"
	"strings"
)

// MessageTokenCount Count the number of tokens in the session context
// TODO Token calculation methods that are not based on the vendor model may be different, and need to be differentiated according to the vendor model
func MessageTokenCount(messages []openai.ChatCompletionMessage, model string) (numTokens int, err error) {
	_model := model

	// 所有非 gpt-3.5-turbo/gpt-4 的模型，都按照 gpt-3.5 的方式处理
	if !array.In(_model, []string{"gpt-3.5-turbo", "gpt-4"}) {
		_model = "gpt-3.5-turbo"
	}

	tkm, err := tiktoken.EncodingForModel(_model)
	if err != nil {
		return 0, fmt.Errorf("EncodingForModel: %v", err)
	}

	var tokensPerMessage int
	if strings.HasPrefix(_model, "gpt-3.5-turbo") {
		tokensPerMessage = 4
	} else if strings.HasPrefix(_model, "gpt-4") {
		tokensPerMessage = 3
	} else {
		tokensPerMessage = 3
	}

	for _, message := range messages {
		numTokens += tokensPerMessage
		if len(message.MultiContent) > 0 {
			for _, content := range message.MultiContent {
				if content.Type == "image_url" {
					// 智谱的 GLM 4V 模型，图片的 token 计算方式不同
					if model == "glm-4v" {
						numTokens += 1047
					} else if strings.HasPrefix(model, "claude-") {
						// Anthropic 的 claude 系列模型，图片的 token 计算方式不同，这里简单处理
						// tokens = (width px * height px)/750
						// https://docs.anthropic.com/claude/docs/vision#image-costs
						numTokens += 1000
					} else {
						if content.ImageURL.Detail == "low" {
							numTokens += 65
						} else {
							// TODO 【价格昂贵，尽量避免】这里可能为 high 或者 auto，简单起见，auto 按照 high 处理
							// 简单起见，这里假设 high 时大图为 2048x2048，切割为 16 个小图
							//
							// high will enable “high res” mode, which first allows the _model to see the low res image
							// and then creates detailed crops of input images as 512px squares based on the input image size.
							// Each of the detailed crops uses twice the token budget (65 tokens) for a total of 129 tokens
							numTokens += 129 * 16
						}
					}

				} else {
					numTokens += len(tkm.Encode(content.Text, nil, nil))
				}
			}
		} else {
			numTokens += len(tkm.Encode(message.Content, nil, nil))
		}
		numTokens += len(tkm.Encode(message.Role, nil, nil))
	}
	numTokens += 3
	return numTokens, nil
}
