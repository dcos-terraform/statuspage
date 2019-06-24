FROM golang:alpine as builder
ENV GOPATH /go
RUN apk add git bash build-base gcc
COPY . $GOPATH/dcos-terraform-statuspage
WORKDIR $GOPATH/dcos-terraform-statuspage
RUN GOOS=linux GOARCH=amd64 GO111MODULE=on go test -coverprofile=coverage.out -v main.go
RUN GOOS=linux GOARCH=amd64 GO111MODULE=on go build -tags static_all -o $GOPATH/bin/dcos-terraform-statuspage -v main.go

FROM alpine:3.9
RUN apk add ca-certificates
COPY --from=builder /go/bin/dcos-terraform-statuspage /usr/local/bin
COPY ./static /static
CMD ["dcos-terraform-statuspage"]
