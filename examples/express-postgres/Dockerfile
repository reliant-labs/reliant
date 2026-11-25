FROM node:22-slim
WORKDIR /srv
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY server.js migrate.js ./
ENV PORT=3000
EXPOSE 3000
USER node
CMD ["node", "server.js"]
