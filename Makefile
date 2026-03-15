BINARY = nsh
MODULE = github.com/InfJoker/nsh

.PHONY: build test clean install

build:
	go build -o $(BINARY) ./cmd/nsh

test:
	go test ./...

clean:
	rm -f $(BINARY)

install: build
	cp $(BINARY) $(GOPATH)/bin/ 2>/dev/null || cp $(BINARY) ~/go/bin/

run: build
	./$(BINARY)
