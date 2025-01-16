package moderation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"golang.org/x/net/proxy"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	server string
	apiKey string
	client *http.Client
}

func New(server, apiKey string, dialer proxy.Dialer) *Client {
	client := &http.Client{}
	if dialer != nil {
		client.Transport = &http.Transport{
			Dial: dialer.Dial,
		}
	}

	return &Client{
		server: server,
		apiKey: apiKey,
		client: client,
	}
}

type Request struct {
	Input []Input `json:"input"`
	Model string  `json:"model"`
}

type Input struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url,omitempty"`
}

type Response struct {
	ID      string   `json:"id,omitempty"`
	Model   string   `json:"model,omitempty"`
	Results []Result `json:"results,omitempty"`
}

func (resp Response) Flagged() bool {
	for _, result := range resp.Results {
		if result.Flagged {
			return true
		}
	}

	return false
}

func (resp Response) FlaggedCategories() []string {
	var categories []string
	for _, result := range resp.Results {
		for category, flagged := range result.Categories {
			if flagged {
				categories = append(categories, category)
			}
		}
	}

	return categories
}

type Result struct {
	Flagged                   bool                `json:"flagged"`
	Categories                map[string]bool     `json:"categories,omitempty"`
	CategoryScores            map[string]float64  `json:"category_scores,omitempty"`
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types,omitempty"`
}

type ErrorResponse struct {
	Error Error `json:"error,omitempty"`
}

type Error struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (client *Client) Moderation(ctx context.Context, req Request) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var result Response

	startTime := time.Now()
	defer func() {
		if log.DebugEnabled() {
			log.Debugf("moderation request completed, duration: %s", time.Since(startTime))
		}
	}()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/v1/moderations", strings.TrimRight(client.server, "/"))
	r, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	r.Header.Set("Authorization", "Bearer "+client.apiKey)

	resp, err := client.client.Do(r)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		respData, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("request failed(%d): %s", resp.StatusCode, resp.Status)
		}

		var errResp ErrorResponse
		if err := json.Unmarshal(respData, &errResp); err != nil {
			return nil, fmt.Errorf("request failed(%d): %s", resp.StatusCode, string(respData))
		}

		return nil, fmt.Errorf("request failed(%s): %s", errResp.Error.Code, errResp.Error.Message)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}
