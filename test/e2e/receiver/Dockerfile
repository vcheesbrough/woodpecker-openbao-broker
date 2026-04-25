FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go ./
RUN go mod init e2e-receiver \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/receiver ./

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/receiver /receiver
EXPOSE 9000
ENTRYPOINT ["/receiver"]
