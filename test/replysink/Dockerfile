FROM golang:1.27@sha256:ec47ea3725fea0cc88f6de94919aef19f40f12fc1ec0e2d1b3426dbf15ecca04 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/replysink ./test/replysink
RUN CGO_ENABLED=0 go build -trimpath -o /out/replysink ./test/replysink

FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /out/replysink /replysink
USER 65532:65532
ENTRYPOINT ["/replysink"]
