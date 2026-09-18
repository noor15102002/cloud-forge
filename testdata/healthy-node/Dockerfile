FROM node:22-alpine
WORKDIR /app
COPY --chown=node:node server.js .
USER node
EXPOSE 8080
CMD ["node", "server.js"]
