package main

import (
	"flag"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/openai-dispatcher/internal"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"net/http"
)

func main() {
	var configFilePath string

	flag.StringVar(&configFilePath, "conf", "config.yaml", "配置文件路径")
	flag.Parse()

	conf, err := config.LoadConfig(configFilePath)
	if err != nil {
		panic(fmt.Errorf("加载配置文件失败：%v", err))
	}

	log.With(conf).Debugf("配置文件加载成功")

	server, err := internal.NewServer(conf)
	if err != nil {
		panic(fmt.Errorf("初始化服务失败：%v", err))
	}

	if err := http.ListenAndServe(conf.Listen, server); err != nil {
		panic(fmt.Errorf("启动服务失败：%v", err))
	}
}
