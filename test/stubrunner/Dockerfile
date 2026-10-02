FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/stubrunner ./test/stubrunner
RUN CGO_ENABLED=0 go build -trimpath -o /out/stubrunner ./test/stubrunner

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/stubrunner /stubrunner
USER 65532:65532
ENTRYPOINT ["/stubrunner"]
