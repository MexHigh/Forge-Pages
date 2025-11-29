# build backend
FROM golang:1.24.10 AS go-builder
WORKDIR /go/src/app
COPY *.go *.mod *.sum .
RUN go get -d -v ./...
RUN CGO_ENABLED=1 GOOS=linux go install -a -ldflags '-linkmode external -extldflags "-static"' .

# small image
FROM scratch
LABEL maintainer="Leon Schmidt"

# Copy CA certs and timezone info
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-builder /usr/share/zoneinfo /usr/share/zoneinfo
# Copy compiled go binary
COPY --from=go-builder /go/bin/forge-pages /forge-pages
# Copy example config
COPY config.example.yml /config.yml

EXPOSE 8080
ENTRYPOINT ["/forge-pages"]
CMD ["--config", "/config.yml"]
