FROM golang:1.23-alpine3.20 AS build
ADD ./src /src
WORKDIR /src
RUN go build -o /opt/go-builder

FROM golang:1.23.2-bookworm
ENV DEBIAN_FRONTEND=noninteractive

RUN apt update

RUN apt install -y gcc
RUN apt install -y libc6-dev
RUN apt install -y libsqlite3-dev
RUN apt install -y pkg-config
RUN apt install -y curl
RUN apt install -y docker.io
RUN apt install -y docker-compose

COPY --from=build /opt/go-builder .

ENTRYPOINT ["./go-builder"]
