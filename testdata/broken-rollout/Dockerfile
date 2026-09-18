FROM node:22-alpine
ARG CLOUDFORGE_VERSION=a
ENV CLOUDFORGE_VERSION=$CLOUDFORGE_VERSION
WORKDIR /app
COPY --chown=node:node server.js .
USER node
EXPOSE 8080
CMD ["node", "server.js"]
