FROM ubuntu:latest
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y golang-go firefox jq bash curl git make bc grep nodejs npm
ENV NPM_CONFIG_CACHE=/tmp/.npm
RUN mkdir -p /app && cd /app
WORKDIR /app
# Initialize npm project and install playwright locally
RUN npm install playwright
RUN npx playwright install firefox && npx playwright install-deps
ENV NODE_PATH=/app/node_modules
# Install go tools
RUN go install github.com/JohannesKaufmann/html-to-markdown/v2/cli/html2markdown@latest
RUN cp /root/go/bin/* /bin/
# Copy scripts
COPY *.sh /bin/
RUN chmod a+x /bin/*.sh