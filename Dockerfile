FROM golang:alpine as builder
ENV GOPATH /go
RUN apk add git bash
COPY . $GOPATH/dcos-terraform-statuspage
WORKDIR $GOPATH/dcos-terraform-statuspage
RUN GOOS=linux GOARCH=amd64 GO111MODULE=on go test -coverprofile=coverage.out -v ./...
RUN GOOS=linux GOARCH=amd64 GO111MODULE=on go build -tags static_all -o $GOPATH/dcos-terraform-statuspage -v main.go

FROM alpine:3.9
COPY --from=builder /go/dcos-terraform-statuspage /
CMD ["dcos-terraform-statuspage"]
