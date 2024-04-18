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

	flag.StringVar(&configFilePath, "conf", "config.yaml", "Configuration file path")
	flag.BoolVar(&configTest, "test", false, "Test profile")
	flag.Parse()

	conf, err := config.LoadConfig(configFilePath)
	if err != nil {
		panic(fmt.Errorf("failed to load the configuration file：%v", err))
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
			panic(fmt.Errorf("configuration file test failed：%v", err))
		}

		fmt.Print("\n-------- Models-Upstreams --------\n\n")
		for model, ups := range upstreams {
			fmt.Println(model)
			ups.Print()
			fmt.Println()
		}

		fmt.Print("\n-------- Default-Upstreams --------\n\n")
		defaultUpstreams.Print()

		return
	}

	log.With(conf).Debugf("The configuration file is successfully loaded")

	server, err := internal.NewServer(conf)
	if err != nil {
		panic(fmt.Errorf("failed to initialize the service：%v", err))
	}

	if conf.EnablePrometheus {
		http.Handle("/metrics", promhttp.Handler())
	}

	http.Handle("/", server)

	if err := http.ListenAndServe(conf.Listen, nil); err != nil {
		panic(fmt.Errorf("service startup failure：%v", err))
	}
}
