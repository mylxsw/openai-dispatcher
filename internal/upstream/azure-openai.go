package upstream

import (
	"golang.org/x/net/proxy"
	"net/http"
)

type AzureOpenAIUpstream struct {
	server     string
	key        string
	apiVersion string
	dialer     proxy.Dialer
}

func NewAzureOpenAIUpstream(server, key, apiVersion string, dialer proxy.Dialer) (*AzureOpenAIUpstream, error) {
	return &AzureOpenAIUpstream{
		dialer:     dialer,
		server:     server,
		key:        key,
		apiVersion: apiVersion,
	}, nil
}

func (target *AzureOpenAIUpstream) Serve(w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error)) {
	//openaiConf := openai.DefaultConfig(target.key)
	//openaiConf.BaseURL = target.server
	//if target.dialer != nil {
	//	openaiConf.HTTPClient.Transport = &http.Transport{Dial: target.dialer.Dial}
	//}
	//openaiConf.APIType = openai.APITypeAzure
	//openaiConf.APIVersion = target.apiVersion
	//
	//client := openai.NewClientWithConfig(openaiConf)
	//
	//

	// TODO 暂未完成

	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error": "Azure 模式正在开发中，敬请期待"}`))
}
