FROM node:22-alpine AS build
WORKDIR /app
COPY . .
RUN npm ci && npm run build

FROM node:22-alpine
USER node
EXPOSE 8080
CMD ["node", "dist/server.js"]

