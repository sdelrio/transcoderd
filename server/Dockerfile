ARG COMPILED_BINARY=./dist/transcoderd-linux-amd64
ARG ENTRYPOINT_BINARY=/bin/transcoderd

FROM golang:1.18.1-alpine as builder
RUN apk update && apk add --no-cache git
WORKDIR $GOPATH/src/transcoder/

COPY go.mod go.sum ./
RUN go mod download

COPY . .

FROM builder as compileprod
RUN go run build.go build server -p linux-amd64
RUN cp ./dist/transcoderd-linux-amd64 /bin/transcoderd && chmod +x /bin/transcoderd

FROM builder as debug
RUN CGO_ENABLED=0 go install -ldflags "-s -w -extldflags '-static'" github.com/go-delve/delve/cmd/dlv@latest
RUN CGO_ENABLED=0 go run -gcflags "all=-N -l" build.go build server -p linux-amd64
RUN cp ./dist/transcoderd-linux-amd64 /bin/transcoderd && chmod +x /bin/transcoderd

ENTRYPOINT [ "/go/bin/dlv", "--check-go-version=false", "--listen=:4000", "--headless=true", "--log=true", "--accept-multiclient", "--api-version=2", "exec", "/bin/transcoderd" ]

FROM scratch as production
COPY --from=compileprod /bin/transcoderd /bin/transcoderd
ENTRYPOINT ["/bin/transcoderd"]
