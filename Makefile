
test-config:
	go run main.go -conf config.yaml -test

run:
	go run main.go -conf config.yaml

build:
	go build -o build/debug/openai-dispatcher main.go

release:
	GOOS=linux GOARCH=amd64 go build -o build/release/openai-dispatcher main.go

clean:
	rm -fr build/

.PHONY: test-config run build release clean