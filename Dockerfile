FROM golang:1.27.0-alpine AS build

LABEL maintainer="pgsty <https://github.com/pgsty/mc>"

ENV GOPATH /go
ENV CGO_ENABLED 0

WORKDIR /src

RUN apk add -U --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Development image by default. Formal releases use .github/goreleaser.yml;
# without caller-supplied LDFLAGS this image reports DEVELOPMENT.GOGET metadata.
ARG LDFLAGS=""
RUN go build -trimpath -tags kqueue \
    -ldflags "${LDFLAGS}" \
    -o /go/bin/mc .

FROM scratch

COPY --from=build /go/bin/mc /usr/bin/mc
COPY --from=build /src/CREDITS /licenses/CREDITS
COPY --from=build /src/LICENSE /licenses/LICENSE
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["mc"]
