package transport

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
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
	url *url.URL
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

		newBody, _ := sjson.Set(string(body), "model", target.replace(gjson.Get(string(body), "model").String()))
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
		log.Debugf("request: %s %s [%d] %v", resp.Request.Method, resp.Request.URL.String(), resp.StatusCode, time.Since(startTime))

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
