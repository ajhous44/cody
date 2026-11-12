# Container profile: cody can change only the project you mount at /workspace.
# Use it where the OS sandbox is unavailable (Windows) or for stronger isolation.
#
#   docker build -t cody .
#   docker run --rm -it -v "$PWD:/workspace" -e OPENROUTER_API_KEY cody
#
# See README "Container profile" for Ollama on the host and the web GUI.

FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /cody .

FROM debian:trixie-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends bash ca-certificates git \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --create-home --uid 1000 cody \
	&& mkdir /workspace && chown cody /workspace
COPY --from=build /cody /usr/local/bin/cody
USER cody
WORKDIR /workspace
# Ollama running on the host; on Linux add --add-host=host.docker.internal:host-gateway.
ENV OLLAMA_HOST=http://host.docker.internal:11434
ENTRYPOINT ["cody"]
