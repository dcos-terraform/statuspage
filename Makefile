.PHONY: version clean test test-with-dep docs build setup clean realclean releasebin version
# Go parameters
GOCMD=go
GO111MODULE=on
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

all:		test build
build:
				mkdir -p bin
				$(GOBUILD) -o bin/dcos-terraform-statuspage -v main.go
test:
				$(GOTEST) -coverprofile=coverage.out -cover -v ./...
clean:
				$(GOCLEAN)
				rm -f bin/dcos-terraform-statuspage
docker-build:
				docker build -f Dockerfile -t dcos-terraform-statuspage:latest .
