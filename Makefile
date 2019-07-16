.PHONY: deps clean build

deps:
	go get -u ./...

clean: 
	rm -rf ./github-hooks/github-hooks
	
build:
	GOOS=linux GOARCH=amd64 go build -o github-hooks/github-hooks ./github-hooks