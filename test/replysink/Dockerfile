FROM golang:1.27@sha256:c292963ad0e5f51e843c726edc204a4e4c26a859ba1e4d736d91028aa3633048 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/replysink ./test/replysink
RUN CGO_ENABLED=0 go build -trimpath -o /out/replysink ./test/replysink

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/replysink /replysink
USER 65532:65532
ENTRYPOINT ["/replysink"]
