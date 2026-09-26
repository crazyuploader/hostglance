FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH BUILDPLATFORM
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o hostglance .

FROM alpine:3.24

RUN apk --no-cache add ca-certificates tzdata wget

COPY --from=builder /app/hostglance /hostglance

EXPOSE 8054

ENTRYPOINT ["/hostglance"]
CMD ["serve"]
