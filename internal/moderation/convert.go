package moderation

import "github.com/sashabaranov/go-openai"

func ConvertChatToRequest(req openai.ChatCompletionRequest, moderationModel string) Request {
	res := Request{
		Model: moderationModel,
		Input: []Input{},
	}

	for _, message := range req.Messages {
		if message.Content != "" {
			res.Input = append(res.Input, Input{
				Type: "text",
				Text: message.Content,
			})
		}

		for _, mul := range message.MultiContent {
			if mul.Type == openai.ChatMessagePartTypeText {
				res.Input = append(res.Input, Input{
					Type: "text",
					Text: mul.Text,
				})
			} else if mul.Type == openai.ChatMessagePartTypeImageURL && mul.ImageURL != nil && mul.ImageURL.URL != "" {
				res.Input = append(res.Input, Input{
					Type: "image_url",
					ImageURL: &ImageURL{
						URL: mul.ImageURL.URL,
					},
				})
			}
		}
	}

	return res
}
