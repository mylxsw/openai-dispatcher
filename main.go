package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/mylxsw/asteria/formatter"
	"github.com/mylxsw/asteria/level"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/asteria/writer"
	"github.com/mylxsw/openai-dispatcher/internal"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"github.com/mylxsw/openai-dispatcher/internal/upstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"path/filepath"
	"time"
)

func main() {
	var configFilePath string
	var configTest bool

	flag.StringVar(&configFilePath, "conf", "config.yaml", "配置文件路径")
	flag.BoolVar(&configTest, "test", false, "测试配置文件")
	flag.Parse()

	conf, err := config.LoadConfig(configFilePath)
	if err != nil {
		panic(fmt.Errorf("加载配置文件失败：%v", err))
	}

	if !configTest && conf.LogPath != "" {
		log.All().LogFormatter(formatter.NewJSONFormatter())
		log.All().LogWriter(writer.NewDefaultRotatingFileWriter(context.TODO(), func(le level.Level, module string) string {
			return filepath.Join(conf.LogPath, fmt.Sprintf("%s.%s.log", le.GetLevelName(), time.Now().Format("20060102")))
		}))
	}

	if configTest {
		upstreams, defaultUpstreams, err := upstream.BuildUpstreamsFromRules(upstream.Policy(conf.Policy), conf.Rules, conf.Validate(), nil)
		if err != nil {
			panic(fmt.Errorf("配置文件测试失败：%v", err))
		}

		fmt.Print("\n-------- 模型-Upstreams --------\n\n")
		for model, ups := range upstreams {
			fmt.Println(model)
			ups.Print()
			fmt.Println()
		}

		fmt.Print("\n-------- 默认-Upstreams --------\n\n")
		defaultUpstreams.Print()

		return
	}

	log.With(conf).Debugf("配置文件加载成功")

	server, err := internal.NewServer(conf)
	if err != nil {
		panic(fmt.Errorf("初始化服务失败：%v", err))
	}

	if conf.EnablePrometheus {
		http.Handle("/metrics", promhttp.Handler())
	}

	http.Handle("/", server)

	if err := http.ListenAndServe(conf.Listen, nil); err != nil {
		panic(fmt.Errorf("启动服务失败：%v", err))
	}
}
