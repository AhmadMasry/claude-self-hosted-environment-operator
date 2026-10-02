FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/replysink ./test/replysink
RUN CGO_ENABLED=0 go build -trimpath -o /out/replysink ./test/replysink

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/replysink /replysink
USER 65532:65532
ENTRYPOINT ["/replysink"]
