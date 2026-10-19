FROM golang:1.27@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/replysink ./test/replysink
RUN CGO_ENABLED=0 go build -trimpath -o /out/replysink ./test/replysink

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/replysink /replysink
USER 65532:65532
ENTRYPOINT ["/replysink"]
