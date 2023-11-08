
build:
	go build -o build/debug/openai-dispatcher main.go

release:
	GOOS=linux GOARCH=amd64 go build -o build/release/openai-dispatcher main.go

clean:
	rm -fr build/

.PHONY: build release clean