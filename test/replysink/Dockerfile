FROM golang:1.26@sha256:0f063af2d465d8dcae54cce04278ada488b96f77b42449c8d071e47d016cc65a AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/replysink ./test/replysink
RUN CGO_ENABLED=0 go build -trimpath -o /out/replysink ./test/replysink

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/replysink /replysink
USER 65532:65532
ENTRYPOINT ["/replysink"]
