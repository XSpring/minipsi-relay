# syntax=docker/dockerfile:1
# Static minipsi binary on a distroless base (non-root). Build:
#   docker build -t minipsi:$(git describe --tags --always) \
#     --build-arg VERSION=$(git describe --tags --always) --build-arg COMMIT=$(git rev-parse --short HEAD) .
# Base images pinned by digest (update deliberately).
FROM golang:1.27.3-bookworm@sha256:5c3e9b43cfd3bb95c7937462e29d87ada57b481a96fa23ad0eb61778d7a00110 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/XSpring/minipsi/internal/version.Version=${VERSION} -X github.com/XSpring/minipsi/internal/version.Commit=${COMMIT}" \
      -o /out/minipsi ./cmd/minipsi

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/minipsi /usr/local/bin/minipsi
USER nonroot:nonroot
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/minipsi"]
