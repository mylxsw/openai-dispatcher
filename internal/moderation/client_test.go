package moderation

import (
	"context"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/assert"
	"os"
	"testing"
	"time"
)

func TestClient_Moderation(t *testing.T) {
	client := New("https://api.openai.com", os.Getenv("OPENAI_API_KEY"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := Request{
		Input: []Input{
			{
				Type: "text",
				Text: "This is a test to see if the model can detect toxic content.",
			},
		},
		Model: "omni-moderation-latest",
	}

	resp, err := client.Moderation(ctx, req)
	assert.NoError(t, err)

	log.With(resp).Infof("response")
}
