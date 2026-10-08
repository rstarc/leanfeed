# Build a static binary, then copy it into a minimal image that runs as a
# non-root user. Build with: docker build --build-arg VERSION=$(git describe --tags --always) -t leanfeed .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /leanfeed ./cmd/leanfeed \
    && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /leanfeed /leanfeed
# A named volume copies this directory's owner, so the non-root user can write to it.
COPY --from=build --chown=nonroot:nonroot /data /data
# Inside a container leanfeed must listen on all interfaces; publish the
# port only where you need it, for example -p 127.0.0.1:8080:8080.
ENV LEANFEED_DATA=/data \
    LEANFEED_ADDR=0.0.0.0:8080
VOLUME /data
EXPOSE 8080
# The image has no HTTP client, so leanfeed checks its own /healthz.
HEALTHCHECK CMD ["/leanfeed", "healthcheck"]
ENTRYPOINT ["/leanfeed"]
CMD ["serve"]
