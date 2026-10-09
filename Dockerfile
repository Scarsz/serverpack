# The serverpack image: just the static binary. Generated server images copy it with
#   COPY --from=ghcr.io/scarsz/serverpack:<version> /serverpack /usr/local/bin/serverpack
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/Scarsz/serverpack/internal/version.Version=${VERSION}" \
    -o /serverpack .

FROM scratch
COPY --from=build /serverpack /serverpack
ENTRYPOINT ["/serverpack"]
