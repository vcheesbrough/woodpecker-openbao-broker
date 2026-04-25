# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /broker .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /broker /broker

USER 65532
EXPOSE 8080
ENTRYPOINT ["/broker"]
