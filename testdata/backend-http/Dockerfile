FROM node:24-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY --chown=node:node server.js prepare.js network-probe.js ./
ARG CLOUDFORGE_VERSION=a
ENV CLOUDFORGE_VERSION=$CLOUDFORGE_VERSION
USER node
EXPOSE 8080
CMD ["node", "server.js"]
